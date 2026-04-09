package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	natsv1alpha1 "github.com/jradikk/nats-auth-operator/api/v1alpha1"
)

const (
	timeout  = 30 * time.Second
	interval = 250 * time.Millisecond
)

// makeAuthConfig creates and returns a minimal NatsAuthConfig in JWT mode.
func makeAuthConfig(name string) *natsv1alpha1.NatsAuthConfig {
	return &natsv1alpha1.NatsAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: natsv1alpha1.NatsAuthConfigSpec{
			NatsURL: "nats://nats:4222",
			Mode:    natsv1alpha1.AuthModeJWT,
			ServerAuthConfig: natsv1alpha1.ServerAuthConfigRef{
				Name:      name + "-jwts",
				Namespace: "default",
				Type:      "Secret",
			},
			JWT: &natsv1alpha1.JWTConfig{
				OperatorName: "TestOperator",
			},
		},
	}
}

// makeAccount creates and returns a NatsAccount referencing the given authconfig.
func makeAccount(name, authConfigName string) *natsv1alpha1.NatsAccount {
	return &natsv1alpha1.NatsAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: natsv1alpha1.NatsAccountSpec{
			AuthConfigRef: natsv1alpha1.NatsAuthConfigRef{
				Name:      authConfigName,
				Namespace: "default",
			},
			Description: "test account",
		},
	}
}

// makeUser creates and returns a NatsUser referencing the given account.
func makeUser(name, authConfigName, accountName string, perms *natsv1alpha1.Permissions) *natsv1alpha1.NatsUser {
	return &natsv1alpha1.NatsUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: natsv1alpha1.NatsUserSpec{
			AuthConfigRef: natsv1alpha1.NatsAuthConfigRef{
				Name:      authConfigName,
				Namespace: "default",
			},
			AuthType: natsv1alpha1.UserAuthTypeJWT,
			AccountRef: &natsv1alpha1.NatsAccountRef{
				Name:      accountName,
				Namespace: "default",
			},
			Username:    name,
			Permissions: perms,
		},
	}
}

// waitReady polls until the NatsAccount has a non-empty AccountID.
func waitAccountReady(key types.NamespacedName) {
	acc := &natsv1alpha1.NatsAccount{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, key, acc)).To(Succeed())
		g.Expect(acc.Status.AccountID).NotTo(BeEmpty())
	}, timeout, interval).Should(Succeed())
}

// waitUserReady polls until the NatsUser has a non-empty PublicKey.
func waitUserReady(key types.NamespacedName) {
	u := &natsv1alpha1.NatsUser{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, key, u)).To(Succeed())
		g.Expect(u.Status.PublicKey).NotTo(BeEmpty())
	}, timeout, interval).Should(Succeed())
}

// deleteAndWait deletes an object and waits for it to disappear from the API.
func deleteAndWait(obj client.Object, key types.NamespacedName) {
	_ = k8sClient.Delete(ctx, obj)
	Eventually(func() error {
		return k8sClient.Get(ctx, key, obj)
	}, timeout, interval).Should(MatchError(ContainSubstring("not found")))
}

// uniqueName generates a unique resource name to avoid cross-test collisions.
var testCounter int

func uniqueName(prefix string) string {
	testCounter++
	return fmt.Sprintf("%s-%d", prefix, testCounter)
}

var _ = Describe("Existing system account is reused when systemAccountName is set", func() {
	It("adopts the pre-existing NatsAccount and does not recreate it", func() {
		acName := uniqueName("reuse-sys-ac")
		existingAccName := uniqueName("existing-sys-acc")

		// Pre-create the system account manually (simulating a pre-upgrade state)
		existingAcc := makeAccount(existingAccName, acName)
		existingAcc.Spec.Description = "manually created system account"

		// Create the authconfig pointing at the existing account name
		ac := makeAuthConfig(acName)
		ac.Spec.JWT.SystemAccountName = existingAccName

		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		Expect(k8sClient.Create(ctx, existingAcc)).To(Succeed())

		accKey := types.NamespacedName{Name: existingAccName, Namespace: "default"}

		DeferCleanup(func() {
			// Delete authconfig first so the account finalizer can complete
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
			deleteAndWait(existingAcc, accKey)
		})

		// Wait for the account to be reconciled and get a public key
		waitAccountReady(accKey)

		// The auto-named account must NOT be created
		autoAcc := &natsv1alpha1.NatsAccount{}
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: acName + "-system-account", Namespace: "default"}, autoAcc)
		}, 5*time.Second, interval).Should(MatchError(ContainSubstring("not found")))

		// The existing account must not have been replaced — description unchanged
		acc := &natsv1alpha1.NatsAccount{}
		Expect(k8sClient.Get(ctx, accKey, acc)).To(Succeed())
		Expect(acc.Spec.Description).To(Equal("manually created system account"))

		// The operator JWT secret must have been written with SystemAccountPubKey set
		Eventually(func(g Gomega) {
			ac := &natsv1alpha1.NatsAuthConfig{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: acName, Namespace: "default"}, ac)).To(Succeed())
			g.Expect(ac.Status.SystemAccountPubKey).NotTo(BeEmpty())

			// Must match the existing account's public key
			a := &natsv1alpha1.NatsAccount{}
			g.Expect(k8sClient.Get(ctx, accKey, a)).To(Succeed())
			g.Expect(ac.Status.SystemAccountPubKey).To(Equal(a.Status.AccountID))
		}, timeout, interval).Should(Succeed())
	})
})

var _ = Describe("System account auto-created by NatsAuthConfig", func() {
	It("creates a NatsAccount named <authconfig>-system-account", func() {
		acName := uniqueName("sys-ac")
		ac := makeAuthConfig(acName)
		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		DeferCleanup(func() {
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
		})

		sysAccKey := types.NamespacedName{Name: acName + "-system-account", Namespace: "default"}
		sysAcc := &natsv1alpha1.NatsAccount{}
		Eventually(func() error {
			return k8sClient.Get(ctx, sysAccKey, sysAcc)
		}, timeout, interval).Should(Succeed())

		Expect(sysAcc.OwnerReferences).To(HaveLen(1))
		Expect(sysAcc.OwnerReferences[0].Name).To(Equal(acName))
	})
})

var _ = Describe("Spec changes re-issue JWTs", func() {
	It("re-issues user JWT when permissions change", func() {
		acName := uniqueName("spec-change-ac")
		accountName := uniqueName("spec-change-acc")
		userName := uniqueName("spec-change-user")

		ac := makeAuthConfig(acName)
		acc := makeAccount(accountName, acName)
		u := makeUser(userName, acName, accountName, &natsv1alpha1.Permissions{
			PublishAllow: []string{"old.>"},
		})

		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		Expect(k8sClient.Create(ctx, acc)).To(Succeed())
		waitAccountReady(types.NamespacedName{Name: accountName, Namespace: "default"})
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		userKey := types.NamespacedName{Name: userName, Namespace: "default"}
		waitUserReady(userKey)

		DeferCleanup(func() {
			deleteAndWait(u, userKey)
			deleteAndWait(acc, types.NamespacedName{Name: accountName, Namespace: "default"})
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
		})

		userCredsKey := types.NamespacedName{Name: userName + "-user-creds", Namespace: "default"}
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, userCredsKey, secret)).To(Succeed())
		originalJWT := string(secret.Data["user.jwt"])
		Expect(originalJWT).NotTo(BeEmpty())

		// Update permissions
		Expect(k8sClient.Get(ctx, userKey, u)).To(Succeed())
		patch := client.MergeFrom(u.DeepCopy())
		u.Spec.Permissions.PublishAllow = []string{"old.>", "new.>"}
		Expect(k8sClient.Patch(ctx, u, patch)).To(Succeed())

		Eventually(func(g Gomega) {
			s := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, userCredsKey, s)).To(Succeed())
			g.Expect(string(s.Data["user.jwt"])).NotTo(Equal(originalJWT))
		}, timeout, interval).Should(Succeed())
	})

	It("re-issues account JWT when limits change", func() {
		acName := uniqueName("spec-change-limits-ac")
		accountName := uniqueName("spec-change-limits-acc")

		ac := makeAuthConfig(acName)
		acc := makeAccount(accountName, acName)
		accKey := types.NamespacedName{Name: accountName, Namespace: "default"}

		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		Expect(k8sClient.Create(ctx, acc)).To(Succeed())
		waitAccountReady(accKey)

		DeferCleanup(func() {
			deleteAndWait(acc, accKey)
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
		})

		accJWTKey := types.NamespacedName{Name: accountName + "-account-jwt", Namespace: "default"}
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, accJWTKey, secret)).To(Succeed())
		originalJWT := string(secret.Data["account.jwt"])
		Expect(originalJWT).NotTo(BeEmpty())

		Expect(k8sClient.Get(ctx, accKey, acc)).To(Succeed())
		patch := client.MergeFrom(acc.DeepCopy())
		if acc.Spec.Limits == nil {
			acc.Spec.Limits = &natsv1alpha1.AccountLimits{}
		}
		acc.Spec.Limits.Conn = 42
		Expect(k8sClient.Patch(ctx, acc, patch)).To(Succeed())

		Eventually(func(g Gomega) {
			s := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, accJWTKey, s)).To(Succeed())
			g.Expect(string(s.Data["account.jwt"])).NotTo(Equal(originalJWT))
		}, timeout, interval).Should(Succeed())
	})
})

var _ = Describe("Force-sync annotation is removed after processing", func() {
	It("removes force-sync annotation from NatsAccount after reconcile", func() {
		acName := uniqueName("fs-ac-acc")
		accountName := uniqueName("fs-acc")

		ac := makeAuthConfig(acName)
		acc := makeAccount(accountName, acName)
		accKey := types.NamespacedName{Name: accountName, Namespace: "default"}

		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		Expect(k8sClient.Create(ctx, acc)).To(Succeed())
		waitAccountReady(accKey)

		DeferCleanup(func() {
			deleteAndWait(acc, accKey)
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
		})

		Expect(k8sClient.Get(ctx, accKey, acc)).To(Succeed())
		patch := client.MergeFrom(acc.DeepCopy())
		if acc.Annotations == nil {
			acc.Annotations = map[string]string{}
		}
		acc.Annotations[ForceSyncAnnotation] = "1"
		Expect(k8sClient.Patch(ctx, acc, patch)).To(Succeed())

		Eventually(func(g Gomega) {
			a := &natsv1alpha1.NatsAccount{}
			g.Expect(k8sClient.Get(ctx, accKey, a)).To(Succeed())
			_, exists := a.Annotations[ForceSyncAnnotation]
			g.Expect(exists).To(BeFalse())
		}, timeout, interval).Should(Succeed())
	})

	It("removes force-sync annotation from NatsUser after reconcile", func() {
		acName := uniqueName("fs-ac-user")
		accountName := uniqueName("fs-user-acc")
		userName := uniqueName("fs-user")

		ac := makeAuthConfig(acName)
		acc := makeAccount(accountName, acName)
		u := makeUser(userName, acName, accountName, nil)
		accKey := types.NamespacedName{Name: accountName, Namespace: "default"}
		userKey := types.NamespacedName{Name: userName, Namespace: "default"}

		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		Expect(k8sClient.Create(ctx, acc)).To(Succeed())
		waitAccountReady(accKey)
		Expect(k8sClient.Create(ctx, u)).To(Succeed())
		waitUserReady(userKey)

		DeferCleanup(func() {
			deleteAndWait(u, userKey)
			deleteAndWait(acc, accKey)
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
		})

		Expect(k8sClient.Get(ctx, userKey, u)).To(Succeed())
		patch := client.MergeFrom(u.DeepCopy())
		if u.Annotations == nil {
			u.Annotations = map[string]string{}
		}
		u.Annotations[ForceSyncAnnotation] = "1"
		Expect(k8sClient.Patch(ctx, u, patch)).To(Succeed())

		Eventually(func(g Gomega) {
			usr := &natsv1alpha1.NatsUser{}
			g.Expect(k8sClient.Get(ctx, userKey, usr)).To(Succeed())
			_, exists := usr.Annotations[ForceSyncAnnotation]
			g.Expect(exists).To(BeFalse())
		}, timeout, interval).Should(Succeed())
	})
})

var _ = Describe("Account keypair stable across re-reconciles", func() {
	It("preserves account public key after force-sync", func() {
		acName := uniqueName("kp-fs-ac")
		accountName := uniqueName("kp-fs-acc")

		ac := makeAuthConfig(acName)
		acc := makeAccount(accountName, acName)
		accKey := types.NamespacedName{Name: accountName, Namespace: "default"}

		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		Expect(k8sClient.Create(ctx, acc)).To(Succeed())
		waitAccountReady(accKey)

		DeferCleanup(func() {
			deleteAndWait(acc, accKey)
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
		})

		Expect(k8sClient.Get(ctx, accKey, acc)).To(Succeed())
		originalPubKey := acc.Status.AccountID

		patch := client.MergeFrom(acc.DeepCopy())
		if acc.Annotations == nil {
			acc.Annotations = map[string]string{}
		}
		acc.Annotations[ForceSyncAnnotation] = "1"
		Expect(k8sClient.Patch(ctx, acc, patch)).To(Succeed())

		// Wait for annotation removal (reconcile complete)
		Eventually(func(g Gomega) {
			a := &natsv1alpha1.NatsAccount{}
			g.Expect(k8sClient.Get(ctx, accKey, a)).To(Succeed())
			_, exists := a.Annotations[ForceSyncAnnotation]
			g.Expect(exists).To(BeFalse())
		}, timeout, interval).Should(Succeed())

		acc2 := &natsv1alpha1.NatsAccount{}
		Expect(k8sClient.Get(ctx, accKey, acc2)).To(Succeed())
		Expect(acc2.Status.AccountID).To(Equal(originalPubKey))
	})

	It("preserves account public key after spec change", func() {
		acName := uniqueName("kp-spec-ac")
		accountName := uniqueName("kp-spec-acc")

		ac := makeAuthConfig(acName)
		acc := makeAccount(accountName, acName)
		accKey := types.NamespacedName{Name: accountName, Namespace: "default"}

		Expect(k8sClient.Create(ctx, ac)).To(Succeed())
		Expect(k8sClient.Create(ctx, acc)).To(Succeed())
		waitAccountReady(accKey)

		DeferCleanup(func() {
			deleteAndWait(acc, accKey)
			deleteAndWait(ac, types.NamespacedName{Name: acName, Namespace: "default"})
		})

		Expect(k8sClient.Get(ctx, accKey, acc)).To(Succeed())
		originalPubKey := acc.Status.AccountID

		patch := client.MergeFrom(acc.DeepCopy())
		acc.Spec.Description = "updated description"
		Expect(k8sClient.Patch(ctx, acc, patch)).To(Succeed())

		Eventually(func(g Gomega) {
			a := &natsv1alpha1.NatsAccount{}
			g.Expect(k8sClient.Get(ctx, accKey, a)).To(Succeed())
			g.Expect(a.Status.ObservedGeneration).To(Equal(a.Generation))
		}, timeout, interval).Should(Succeed())

		acc2 := &natsv1alpha1.NatsAccount{}
		Expect(k8sClient.Get(ctx, accKey, acc2)).To(Succeed())
		Expect(acc2.Status.AccountID).To(Equal(originalPubKey))
	})
})
