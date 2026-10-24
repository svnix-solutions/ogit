package application

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// SSH key rules:
//   - accepted: ed25519, ECDSA (P-256/384/521), RSA >= 3072 bits, and the
//     FIDO variants sk-ed25519 / sk-ecdsa;
//   - a key (by SHA256 fingerprint) is used in one place only: one user, or
//     one repository as a deploy key;
//   - users manage their own keys; when SMTP is configured they are emailed
//     when a key is added;
//   - deploy keys are managed by the repository's maintainers and owners;
//   - admins can list and delete any user's keys, not add them.

const MinRSAKeyBits = 3072

var (
	ErrInvalidSSHKey  = errors.New("invalid SSH public key")
	ErrSSHKeyInUse    = errors.New("this SSH key is already in use")
	ErrSSHKeyName     = errors.New("an SSH key with this name already exists")
	ErrSSHKeyNotFound = errors.New("SSH key not found")
)

type SSHKeyApp struct {
	Config        config.Config
	Logger        zerolog.Logger
	SSHKeyPers    domain.SSHKeyPers
	UserPers      domain.UserPers
	RepositoryApp *RepositoryApp
	AdminApp      *AdminApp
	Mailer        mail.Sender
}

// ParsedSSHKey is a validated public key.
type ParsedSSHKey struct {
	PublicKey   ssh.PublicKey
	Type        domain.SSHType
	Fingerprint string
	Comment     string
}

// ParseSSHKey parses one authorized_keys line and applies the type rules.
func ParseSSHKey(text string) (*ParsedSSHKey, error) {
	pub, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(text)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSSHKey, err)
	}
	if len(strings.TrimSpace(string(rest))) > 0 {
		return nil, fmt.Errorf("%w: expected a single key", ErrInvalidSSHKey)
	}

	var keyType domain.SSHType
	switch pub.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519:
		keyType = domain.SSHTypeED25519
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKECDSA256:
		keyType = domain.SSHTypeECDSA
	case ssh.KeyAlgoRSA:
		crypto, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return nil, ErrInvalidSSHKey
		}
		rsaKey, ok := crypto.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || rsaKey.N.BitLen() < MinRSAKeyBits {
			return nil, fmt.Errorf("%w: RSA keys must have at least %d bits", ErrInvalidSSHKey, MinRSAKeyBits)
		}
		keyType = domain.SSHTypeRSA
	default:
		return nil, fmt.Errorf("%w: unsupported key type %s (use ed25519, ECDSA or RSA >= %d bits)", ErrInvalidSSHKey, pub.Type(), MinRSAKeyBits)
	}

	return &ParsedSSHKey{PublicKey: pub, Type: keyType, Fingerprint: ssh.FingerprintSHA256(pub), Comment: comment}, nil
}

func (c *SSHKeyApp) newKey(parsed *ParsedSSHKey, name, createdBy string) (*domain.SSHKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = parsed.Comment
	}
	if name == "" || len(name) > 100 {
		return nil, fmt.Errorf("%w: a name (1-100 characters) is required", ErrInvalidSSHKey)
	}
	return &domain.SSHKey{
		Id:          utils.UUIDv4(),
		Name:        name,
		Fingerprint: parsed.Fingerprint,
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed.PublicKey))),
		KeyType:     parsed.Type,
		CreatedBy:   &createdBy,
	}, nil
}

func (c *SSHKeyApp) create(key *domain.SSHKey) error {
	// The fingerprint is checked first so that a reused key reports
	// ErrSSHKeyInUse rather than a name clash.
	if _, err := c.SSHKeyPers.GetByFingerprint(key.Fingerprint); err == nil {
		return ErrSSHKeyInUse
	} else if !isNotFound(err) {
		return err
	}
	err := c.SSHKeyPers.Create(key)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		if strings.Contains(strings.ToLower(err.Error()), "fingerprint") {
			return ErrSSHKeyInUse
		}
		return ErrSSHKeyName
	}
	return err
}

// AddUserKey adds a key to userId's account.
func (c *SSHKeyApp) AddUserKey(userId, name, text string) (*domain.SSHKey, error) {
	parsed, err := ParseSSHKey(text)
	if err != nil {
		return nil, err
	}
	key, err := c.newKey(parsed, name, userId)
	if err != nil {
		return nil, err
	}
	key.OwnerType = domain.SSHKeyTypeUser
	key.UserId = &userId
	key.ReadOnly = false
	if err := c.create(key); err != nil {
		return nil, err
	}
	c.notifyKeyAdded(userId, key)
	return key, nil
}

func (c *SSHKeyApp) ListUserKeys(userId string) ([]domain.SSHKey, error) {
	return c.SSHKeyPers.ListForUser(userId)
}

func (c *SSHKeyApp) DeleteUserKey(userId, keyId string) error {
	if err := c.SSHKeyPers.DeleteForUser(userId, keyId); isNotFound(err) {
		return ErrSSHKeyNotFound
	} else if err != nil {
		return err
	}
	return nil
}

// AddDeployKey adds a deploy key to a repository the actor maintains.
func (c *SSHKeyApp) AddDeployKey(id, slug *string, actorId, name, text string, readOnly bool) (*domain.SSHKey, error) {
	repo, err := c.repositoryForMaintainer(id, slug, actorId)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseSSHKey(text)
	if err != nil {
		return nil, err
	}
	key, err := c.newKey(parsed, name, actorId)
	if err != nil {
		return nil, err
	}
	key.OwnerType = domain.SSHKeyTypeRepository
	key.RepositoryId = &repo.Id
	key.ReadOnly = readOnly
	if err := c.create(key); err != nil {
		return nil, err
	}
	return key, nil
}

func (c *SSHKeyApp) ListDeployKeys(id, slug *string, actorId string) ([]domain.SSHKey, error) {
	repo, err := c.repositoryForMaintainer(id, slug, actorId)
	if err != nil {
		return nil, err
	}
	return c.SSHKeyPers.ListForRepository(repo.Id)
}

func (c *SSHKeyApp) DeleteDeployKey(id, slug *string, actorId, keyId string) error {
	repo, err := c.repositoryForMaintainer(id, slug, actorId)
	if err != nil {
		return err
	}
	if err := c.SSHKeyPers.DeleteForRepository(repo.Id, keyId); isNotFound(err) {
		return ErrSSHKeyNotFound
	} else if err != nil {
		return err
	}
	return nil
}

// AdminListUserKeys and AdminDeleteUserKey let admins revoke keys.
func (c *SSHKeyApp) AdminListUserKeys(actorId, username string) ([]domain.SSHKey, error) {
	user, err := c.adminTarget(actorId, username)
	if err != nil {
		return nil, err
	}
	return c.SSHKeyPers.ListForUser(user.Id)
}

func (c *SSHKeyApp) AdminDeleteUserKey(actorId, username, keyId string) error {
	user, err := c.adminTarget(actorId, username)
	if err != nil {
		return err
	}
	if err := c.DeleteUserKey(user.Id, keyId); err != nil {
		return err
	}
	c.AdminApp.audit(actorId, "ssh_key.delete").Str("user_id", user.Id).Str("key_id", keyId).Send()
	return nil
}

func (c *SSHKeyApp) adminTarget(actorId, username string) (*domain.User, error) {
	if err := c.AdminApp.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	return c.AdminApp.userByUsername(username)
}

// repositoryForMaintainer returns the repository if actorId is a maintainer
// or owner (directly or via a group). Non-readers get not found.
func (c *SSHKeyApp) repositoryForMaintainer(id, slug *string, actorId string) (*domain.Repository, error) {
	repo, role, err := c.RepositoryApp.repositoryForManager(id, slug, actorId)
	if err != nil {
		return nil, err
	}
	if !role.CanAdmin() {
		return nil, ErrForbidden
	}
	return repo, nil
}

// notifyKeyAdded emails the user, when SMTP is configured. Failures are only
// logged: the key is added either way.
func (c *SSHKeyApp) notifyKeyAdded(userId string, key *domain.SSHKey) {
	if c.Config.SMTP.Host == "" || c.Mailer == nil {
		return
	}
	user, err := c.UserPers.GetById(userId)
	if err != nil {
		return
	}
	body := fmt.Sprintf("Hello %s,\n\n"+
		"A new SSH key was added to your oGit account:\n\n"+
		"  %s\n  %s\n\n"+
		"If you did not add it, remove it right away and change your password.\n",
		user.Username, key.Name, key.Fingerprint)
	if err := c.Mailer.Send(user.Email, "A new SSH key was added to your account", body); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.ssh_key").Str("user_id", userId).Msg("failed to send SSH key notification")
	}
}

// SSHIdentity is who an authenticated SSH key acts as.
type SSHIdentity struct {
	Key  *domain.SSHKey
	Name string // username, or "deploy key <name>"
}

// IdentifySSHKey resolves an offered public key during the SSH handshake.
// Unknown keys and keys of disabled (or, when required, unverified) users
// are rejected.
func (c *AuthApp) IdentifySSHKey(keys domain.SSHKeyPers, fingerprint string) (*SSHIdentity, error) {
	key, err := keys.GetByFingerprint(fingerprint)
	if err != nil {
		return nil, ErrGitUnauthenticated
	}
	switch key.OwnerType {
	case domain.SSHKeyTypeUser:
		if key.UserId == nil || !key.User.Active {
			return nil, ErrGitUnauthenticated
		}
		if c.Config.Registration.RequireEmailVerification && key.User.EmailVerifiedAt == nil {
			return nil, ErrGitUnauthenticated
		}
		return &SSHIdentity{Key: key, Name: key.User.Username}, nil
	case domain.SSHKeyTypeRepository:
		if key.RepositoryId == nil {
			return nil, ErrGitUnauthenticated
		}
		return &SSHIdentity{Key: key, Name: "deploy key " + key.Name}, nil
	default:
		return nil, ErrGitUnauthenticated
	}
}

// AuthorizeSSH decides whether an identified key may run operation
// ("git-upload-pack" or "git-receive-pack") on the repository, with the same
// outcomes as AuthorizeGit: private repositories the key cannot read are
// reported as not found.
func (c *AuthApp) AuthorizeSSH(id *SSHIdentity, repoPath, operation string) (*domain.Repository, error) {
	var write bool
	switch operation {
	case "git-upload-pack":
	case "git-receive-pack":
		write = true
	default:
		return nil, fmt.Errorf("invalid git operation: %s", operation)
	}

	repo, err := c.RepositoryApp.GetRepository(nil, &repoPath)
	if err != nil {
		return nil, ErrGitNotFound
	}

	switch id.Key.OwnerType {
	case domain.SSHKeyTypeUser:
		canRead, err := c.RepositoryApp.CanUserAccess(repo.Id, *id.Key.UserId, "read")
		if err != nil {
			return nil, err
		}
		if !canRead {
			return nil, ErrGitNotFound
		}
		if write {
			canWrite, err := c.RepositoryApp.CanUserAccess(repo.Id, *id.Key.UserId, "write")
			if err != nil {
				return nil, err
			}
			if !canWrite {
				return nil, ErrGitForbidden
			}
		}
	case domain.SSHKeyTypeRepository:
		if *id.Key.RepositoryId != repo.Id {
			return nil, ErrGitNotFound
		}
		if write && id.Key.ReadOnly {
			return nil, ErrGitForbidden
		}
	default:
		return nil, ErrGitForbidden
	}
	if write && repo.IsArchived {
		return nil, ErrGitArchived
	}

	return repo, nil
}
