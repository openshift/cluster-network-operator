package bootstrapauthenticator

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"k8s.io/klog"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	// BootstrapUser is the magic bootstrap OAuth user that can perform any action
	BootstrapUser = "kube:admin"
	// support basic auth which does not allow : in username
	bootstrapUserBasicAuth = "kubeadmin"
	// force the use of a secure password length
	// expected format is 5char-5char-5char-5char
	minPasswordLen = 23
)

var (
	// make it obvious that we refuse to honor short passwords
	errPasswordTooShort = fmt.Errorf("%s password must be at least %d characters long", bootstrapUserBasicAuth, minPasswordLen)

	// we refuse to honor a secret that is too new when compared to kube-system
	// since kube-system always exists and cannot be deleted
	// and creation timestamp is controlled by the api, we can use this to
	// detect if the secret was recreated after the initial bootstrapping
	errSecretRecreated = fmt.Errorf("%s secret cannot be recreated", bootstrapUserBasicAuth)
)

func New(getter BootstrapUserDataGetter) authenticator.Password {
	return &bootstrapPassword{
		getter: getter,
		names:  sets.NewString(BootstrapUser, bootstrapUserBasicAuth),
	}
}

type bootstrapPassword struct {
	getter BootstrapUserDataGetter
	names  sets.String
}

func (b *bootstrapPassword) AuthenticatePassword(ctx context.Context, username, password string) (*authenticator.Response, bool, error) {
	if !b.names.Has(username) {
		return nil, false, nil
	}

	data, ok, err := b.getter.Get()
	if err != nil || !ok {
		return nil, ok, err
	}

	// check length after we know that the secret is functional since
	// we do not want to complain when the bootstrap user is disabled
	if len(password) < minPasswordLen {
		return nil, false, errPasswordTooShort
	}

	if err := bcrypt.CompareHashAndPassword(data.PasswordHash, []byte(password)); err != nil {
		if err == bcrypt.ErrMismatchedHashAndPassword {
			klog.V(4).Infof("%s password mismatch", bootstrapUserBasicAuth)
			return nil, false, nil
		}
		return nil, false, err
	}

	// do not set other fields, see identitymapper.userToInfo func
	return &authenticator.Response{
		User: &user.DefaultInfo{
			Name: BootstrapUser,
			UID:  data.UID, // uid ties this authentication to the current state of the secret
		},
	}, true, nil
}

type BootstrapUserData struct {
	PasswordHash []byte
	UID          string
}

type BootstrapUserDataGetter interface {
	Get() (data *BootstrapUserData, ok bool, err error)
	// TODO add a method like:
	// IsPermanentlyDisabled() bool
	// and use it to gate the wiring of components related to the bootstrap user.
	// when the oauth server is running embedded in the kube api server, this method would always
	// return false because the control plane would not be functional at the time of the check.
	// when running as an external process, we can assume a functional control plane to perform the check.
}

func NewBootstrapUserDataGetter(secrets v1.SecretsGetter, namespaces v1.NamespacesGetter) BootstrapUserDataGetter {
	return &bootstrapUserDataGetter{
		secrets:    secrets.Secrets(metav1.NamespaceSystem),
		namespaces: namespaces.Namespaces(),
	}
}

type bootstrapUserDataGetter struct {
	secrets    v1.SecretInterface
	namespaces v1.NamespaceInterface
}

func (b *bootstrapUserDataGetter) Get() (*BootstrapUserData, bool, error) {
	secret, err := b.secrets.Get(bootstrapUserBasicAuth, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		klog.V(4).Infof("%s secret does not exist", bootstrapUserBasicAuth)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if secret.DeletionTimestamp != nil {
		klog.V(4).Infof("%s secret is being deleted", bootstrapUserBasicAuth)
		return nil, false, nil
	}
	namespace, err := b.namespaces.Get(metav1.NamespaceSystem, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}
	if secret.CreationTimestamp.After(namespace.CreationTimestamp.Add(time.Hour)) {
		return nil, false, errSecretRecreated
	}

	hashedPassword := secret.Data[bootstrapUserBasicAuth]

	// make sure the value is a valid bcrypt hash
	if _, err := bcrypt.Cost(hashedPassword); err != nil {
		return nil, false, err
	}

	exactSecret := string(secret.UID) + secret.ResourceVersion
	both := append([]byte(exactSecret), hashedPassword...)

	// use a hash to avoid leaking any derivative of the password
	// this makes it easy for us to tell if the secret changed
	uidBytes := sha512.Sum512(both)

	return &BootstrapUserData{
		PasswordHash: hashedPassword,
		UID:          base64.RawURLEncoding.EncodeToString(uidBytes[:]),
	}, true, nil
}
