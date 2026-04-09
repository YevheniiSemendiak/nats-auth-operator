/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	natsv1alpha1 "github.com/jradikk/nats-auth-operator/api/v1alpha1"
	"github.com/jradikk/nats-auth-operator/internal/authconf"
	jwtpkg "github.com/jradikk/nats-auth-operator/internal/jwt"
	"github.com/jradikk/nats-auth-operator/internal/resolver"
)

const (
	natsAuthConfigFinalizer = "nats.jradikk/authconfig-finalizer"

	// ForceSyncAnnotation can be applied to any CRD to force an immediate full reconcile,
	// bypassing idempotency guards. The controller removes the annotation after processing.
	// Usage: kubectl annotate NatsAccount my-acc nats.jradikk/force-sync=$(date +%s) --overwrite
	ForceSyncAnnotation = "nats.jradikk/force-sync"
)

// NatsAuthConfigReconciler reconciles a NatsAuthConfig object
type NatsAuthConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=nats.jradikk,resources=natsauthconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nats.jradikk,resources=natsauthconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nats.jradikk,resources=natsauthconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=nats.jradikk,resources=natsaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *NatsAuthConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the NatsAuthConfig instance
	authConfig := &natsv1alpha1.NatsAuthConfig{}
	if err := r.Get(ctx, req.NamespacedName, authConfig); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !authConfig.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, authConfig)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(authConfig, natsAuthConfigFinalizer) {
		controllerutil.AddFinalizer(authConfig, natsAuthConfigFinalizer)
		if err := r.Update(ctx, authConfig); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle force-sync annotation
	if _, ok := authConfig.Annotations[ForceSyncAnnotation]; ok {
		patch := client.MergeFrom(authConfig.DeepCopy())
		delete(authConfig.Annotations, ForceSyncAnnotation)
		if err := r.Patch(ctx, authConfig, patch); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Force-sync annotation detected, running full reconcile")
	}

	// Validate the spec
	if err := r.validateSpec(authConfig); err != nil {
		log.Error(err, "Invalid spec")
		base := authConfig.DeepCopy()
		r.updateCondition(authConfig, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "InvalidSpec",
			Message: err.Error(),
		})
		if err := r.Status().Patch(ctx, authConfig, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Snapshot status before reconcile so we can patch only what changed,
	// avoiding resourceVersion conflicts with concurrent annotation writes.
	statusBase := authConfig.DeepCopy()

	// Reconcile based on mode
	var reconcileErr error
	switch authConfig.Spec.Mode {
	case natsv1alpha1.AuthModeJWT:
		reconcileErr = r.reconcileJWTMode(ctx, authConfig)
	case natsv1alpha1.AuthModeToken:
		reconcileErr = r.reconcileTokenMode(ctx, authConfig)
	case natsv1alpha1.AuthModeMixed:
		reconcileErr = r.reconcileMixedMode(ctx, authConfig)
	default:
		reconcileErr = fmt.Errorf("unsupported auth mode: %s", authConfig.Spec.Mode)
	}

	// Patch status
	now := metav1.Now()
	authConfig.Status.LastReconciled = &now
	authConfig.Status.ObservedGeneration = authConfig.Generation

	if reconcileErr != nil {
		log.Error(reconcileErr, "Failed to reconcile")
		r.updateCondition(authConfig, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "ReconcileError",
			Message: reconcileErr.Error(),
		})
		if err := r.Status().Patch(ctx, authConfig, client.MergeFrom(statusBase)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, reconcileErr
	}

	r.updateCondition(authConfig, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "ReconcileSuccess",
		Message: "NatsAuthConfig reconciled successfully",
	})

	if err := r.Status().Patch(ctx, authConfig, client.MergeFrom(statusBase)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *NatsAuthConfigReconciler) validateSpec(authConfig *natsv1alpha1.NatsAuthConfig) error {
	if authConfig.Spec.Mode == natsv1alpha1.AuthModeJWT || authConfig.Spec.Mode == natsv1alpha1.AuthModeMixed {
		if authConfig.Spec.JWT == nil {
			return fmt.Errorf("JWT configuration is required for JWT or mixed mode")
		}
	}
	return nil
}

func (r *NatsAuthConfigReconciler) reconcileJWTMode(ctx context.Context, authConfig *natsv1alpha1.NatsAuthConfig) error {
	log := log.FromContext(ctx)

	// Get or create operator seed
	operatorSeed, err := r.getOrCreateOperatorSeed(ctx, authConfig)
	if err != nil {
		return fmt.Errorf("failed to get operator seed: %w", err)
	}

	// Create operator manager
	operatorName := "NATS Operator"
	if authConfig.Spec.JWT.OperatorName != "" {
		operatorName = authConfig.Spec.JWT.OperatorName
	}

	operatorMgr, err := jwtpkg.NewOperatorManager(operatorSeed, operatorName)
	if err != nil {
		return fmt.Errorf("failed to create operator manager: %w", err)
	}

	// Get operator public key
	operatorPubKey, err := operatorMgr.GetPublicKey()
	if err != nil {
		return fmt.Errorf("failed to get operator public key: %w", err)
	}

	// Ensure the system account NatsAccount resource exists; create it if not.
	// Default to "<authconfig-name>-system-account" to avoid name collisions
	// when multiple NatsAuthConfig resources exist in the same namespace.
	systemAccountName := authConfig.Name + "-system-account"
	if authConfig.Spec.JWT.SystemAccountName != "" {
		systemAccountName = authConfig.Spec.JWT.SystemAccountName
	}
	if err := r.ensureSystemAccount(ctx, authConfig, systemAccountName); err != nil {
		return fmt.Errorf("failed to ensure system account: %w", err)
	}

	// Collect all account JWTs (includes the system account once it has reconciled)
	accounts, err := r.collectAccountJWTs(ctx, authConfig)
	if err != nil {
		return fmt.Errorf("failed to collect account JWTs: %w", err)
	}

	// Embed system account into operator JWT by looking it up by name.
	// This makes the trust chain self-contained so the server shows the system
	// account name under "Trusted Operators" instead of an empty string.
	for _, acc := range accounts {
		if acc.AccountName == systemAccountName {
			if err := operatorMgr.SetSystemAccount(acc.AccountID); err != nil {
				return fmt.Errorf("failed to set system account on operator JWT: %w", err)
			}
			log.Info("Embedded system account in operator JWT", "accountID", acc.AccountID)
			authConfig.Status.SystemAccountPubKey = acc.AccountID
			break
		}
	}
	if authConfig.Status.SystemAccountPubKey == "" {
		log.Info("System account JWT not ready yet — will embed on next reconcile", "systemAccount", systemAccountName)
	}

	// Build Secret data with individual JWT keys
	secretData := map[string][]byte{
		"operator": []byte(operatorMgr.GetJWT()),
	}

	// Add each account JWT as a separate key (using account name for readability)
	for _, acc := range accounts {
		// Use account name as the key (e.g., "rumpusaccount", "system-account")
		secretData[acc.AccountName] = []byte(acc.JWT)
	}

	// Create or update the Secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      authConfig.Spec.ServerAuthConfig.Name,
			Namespace: authConfig.Spec.ServerAuthConfig.Namespace,
		},
		Data: secretData,
	}

	// Try to get existing secret
	existingSecret := &corev1.Secret{}
	err = r.Get(ctx, client.ObjectKey{
		Namespace: authConfig.Spec.ServerAuthConfig.Namespace,
		Name:      authConfig.Spec.ServerAuthConfig.Name,
	}, existingSecret)

	if err != nil {
		if errors.IsNotFound(err) {
			// Create new secret
			if err := r.Create(ctx, secret); err != nil {
				return fmt.Errorf("failed to create JWT secret: %w", err)
			}
			log.Info("Created JWT secret", "name", secret.Name, "accounts", len(accounts))
		} else {
			return fmt.Errorf("failed to get existing secret: %w", err)
		}
	} else {
		// Update existing secret
		existingSecret.Data = secretData
		if err := r.Update(ctx, existingSecret); err != nil {
			return fmt.Errorf("failed to update JWT secret: %w", err)
		}
		log.Info("Updated JWT secret", "name", secret.Name, "accounts", len(accounts))
	}

	// Update status
	authConfig.Status.OperatorPubKey = operatorPubKey
	authConfig.Status.ResolverReady = true

	log.Info("JWT mode reconciled successfully", "operatorPubKey", operatorPubKey, "accounts", len(accounts))

	return nil
}

func (r *NatsAuthConfigReconciler) reconcileTokenMode(ctx context.Context, authConfig *natsv1alpha1.NatsAuthConfig) error {
	// In token mode, we just write an empty auth config initially
	// Users will be added by the NatsUser controller
	authConf := authconf.RenderTokenAuthConf([]authconf.TokenUser{})

	if err := resolver.WriteResolverConfig(
		ctx,
		r.Client,
		authConfig.Spec.ServerAuthConfig.Namespace,
		authConfig.Spec.ServerAuthConfig.Name,
		authConfig.Spec.ServerAuthConfig.Key,
		authConfig.Spec.ServerAuthConfig.Type,
		authConf,
	); err != nil {
		return fmt.Errorf("failed to write token auth config: %w", err)
	}

	authConfig.Status.ResolverReady = true

	return nil
}

func (r *NatsAuthConfigReconciler) reconcileMixedMode(ctx context.Context, authConfig *natsv1alpha1.NatsAuthConfig) error {
	// For mixed mode, we need both JWT and token configs
	// First setup JWT infrastructure
	if err := r.reconcileJWTMode(ctx, authConfig); err != nil {
		return err
	}

	// Token users will be added by the NatsUser controller
	return nil
}

// collectAccountJWTs retrieves all account JWTs associated with this NatsAuthConfig
func (r *NatsAuthConfigReconciler) collectAccountJWTs(ctx context.Context, authConfig *natsv1alpha1.NatsAuthConfig) ([]authconf.AccountJWT, error) {
	log := log.FromContext(ctx)

	// List all NatsAccounts that reference this NatsAuthConfig
	accountList := &natsv1alpha1.NatsAccountList{}
	if err := r.List(ctx, accountList, client.InNamespace(authConfig.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}

	var accounts []authconf.AccountJWT

	for _, account := range accountList.Items {
		// Check if this account references our NatsAuthConfig
		if account.Spec.AuthConfigRef.Name != authConfig.Name {
			continue
		}

		// Get the account JWT from the secret
		secretName := fmt.Sprintf("%s-account-jwt", account.Name)
		secret := &corev1.Secret{}
		key := client.ObjectKey{
			Namespace: account.Namespace,
			Name:      secretName,
		}

		if err := r.Get(ctx, key, secret); err != nil {
			if errors.IsNotFound(err) {
				log.Info("Account JWT secret not found yet, skipping", "account", account.Name)
				continue
			}
			return nil, fmt.Errorf("failed to get account JWT secret: %w", err)
		}

		// Extract JWT and account ID
		jwtData, ok := secret.Data["account.jwt"]
		if !ok {
			log.Info("Account JWT not found in secret, skipping", "account", account.Name)
			continue
		}

		// Get account ID from status
		if account.Status.AccountID == "" {
			log.Info("Account ID not set in status yet, skipping", "account", account.Name)
			continue
		}

		accounts = append(accounts, authconf.AccountJWT{
			AccountName: account.Name,
			AccountID:   account.Status.AccountID,
			JWT:         string(jwtData),
		})
	}

	log.Info("Collected account JWTs", "count", len(accounts))
	return accounts, nil
}

func (r *NatsAuthConfigReconciler) getOrCreateOperatorSeed(ctx context.Context, authConfig *natsv1alpha1.NatsAuthConfig) ([]byte, error) {
	// Check if existing seed is specified
	if authConfig.Spec.JWT.OperatorSeedSecret != nil {
		secret := &corev1.Secret{}
		key := client.ObjectKey{
			Namespace: authConfig.Spec.JWT.OperatorSeedSecret.Namespace,
			Name:      authConfig.Spec.JWT.OperatorSeedSecret.Name,
		}
		if err := r.Get(ctx, key, secret); err != nil {
			return nil, fmt.Errorf("failed to get operator seed secret: %w", err)
		}

		seedKey := authConfig.Spec.JWT.OperatorSeedSecret.Key
		if seedKey == "" {
			seedKey = "operator.seed"
		}

		seed, ok := secret.Data[seedKey]
		if !ok {
			return nil, fmt.Errorf("operator seed key %q not found in secret", seedKey)
		}

		return seed, nil
	}

	// Create new operator and store the seed
	operatorMgr, err := jwtpkg.NewOperatorManager(nil, "NATS Operator")
	if err != nil {
		return nil, err
	}

	seed, err := operatorMgr.GetSeed()
	if err != nil {
		return nil, err
	}

	// Store the seed in a secret
	secretName := fmt.Sprintf("%s-operator-seed", authConfig.Name)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: authConfig.Namespace,
		},
		Data: map[string][]byte{
			"operator.seed": seed,
		},
	}

	if err := controllerutil.SetControllerReference(authConfig, secret, r.Scheme); err != nil {
		return nil, err
	}

	if err := r.Create(ctx, secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			return nil, err
		}
		// If already exists, retrieve it
		if err := r.Get(ctx, client.ObjectKey{Namespace: authConfig.Namespace, Name: secretName}, secret); err != nil {
			return nil, err
		}
		return secret.Data["operator.seed"], nil
	}

	return seed, nil
}

// ensureSystemAccount creates the system NatsAccount if it does not already exist.
// If the account was created by the user before this version of the operator, it is
// left untouched — the operator will still use it as the system account.
func (r *NatsAuthConfigReconciler) ensureSystemAccount(ctx context.Context, authConfig *natsv1alpha1.NatsAuthConfig, name string) error {
	log := log.FromContext(ctx)

	existing := &natsv1alpha1.NatsAccount{}
	err := r.Get(ctx, client.ObjectKey{Namespace: authConfig.Namespace, Name: name}, existing)
	if err == nil {
		// Already exists — nothing to do
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check for system account: %w", err)
	}

	account := &natsv1alpha1.NatsAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: authConfig.Namespace,
		},
		Spec: natsv1alpha1.NatsAccountSpec{
			AuthConfigRef: natsv1alpha1.NatsAuthConfigRef{
				Name:      authConfig.Name,
				Namespace: authConfig.Namespace,
			},
			Description: "System account — managed by NatsAuthConfig, do not delete",
		},
	}

	if err := controllerutil.SetControllerReference(authConfig, account, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, account); err != nil {
		return fmt.Errorf("failed to create system account: %w", err)
	}

	log.Info("Auto-created system account", "name", name)
	return nil
}

func (r *NatsAuthConfigReconciler) handleDeletion(ctx context.Context, authConfig *natsv1alpha1.NatsAuthConfig) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(authConfig, natsAuthConfigFinalizer) {
		// Cleanup logic here if needed
		controllerutil.RemoveFinalizer(authConfig, natsAuthConfigFinalizer)
		if err := r.Update(ctx, authConfig); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *NatsAuthConfigReconciler) updateCondition(authConfig *natsv1alpha1.NatsAuthConfig, condition metav1.Condition) {
	condition.LastTransitionTime = metav1.Now()
	found := false
	for i, c := range authConfig.Status.Conditions {
		if c.Type == condition.Type {
			authConfig.Status.Conditions[i] = condition
			found = true
			break
		}
	}
	if !found {
		authConfig.Status.Conditions = append(authConfig.Status.Conditions, condition)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *NatsAuthConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&natsv1alpha1.NatsAuthConfig{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&natsv1alpha1.NatsAccount{}).
		Complete(r)
}
