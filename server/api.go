package server

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/coreos/dex/api"
	"github.com/coreos/dex/server/internal"
	"github.com/coreos/dex/storage"
	"github.com/coreos/dex/version"
)

// apiVersion increases every time a new call is added to the API. Clients should use this info
// to determine if the server supports specific features.
const apiVersion = 1

// NewAPI returns a server which implements the gRPC API interface.
func NewAPI(s storage.Storage, logger logrus.FieldLogger) api.DexServer {
	return dexAPI{
		s:      s,
		logger: logger,
	}
}

type dexAPI struct {
	s      storage.Storage
	logger logrus.FieldLogger
}

func (d dexAPI) CreateClient(ctx context.Context, req *api.CreateClientReq) (*api.CreateClientResp, error) {
	if req.Client == nil {
		return nil, errors.New("no client supplied")
	}

	if req.Client.Id == "" {
		req.Client.Id = storage.NewID()
	}
	if req.Client.Secret == "" {
		req.Client.Secret = storage.NewID() + storage.NewID()
	}

	c := storage.Client{
		ID:           req.Client.Id,
		Secret:       req.Client.Secret,
		RedirectURIs: req.Client.RedirectUris,
		TrustedPeers: req.Client.TrustedPeers,
		Public:       req.Client.Public,
		Name:         req.Client.Name,
		LogoURL:      req.Client.LogoUrl,
	}
	if err := d.s.CreateClient(c); err != nil {
		if err == storage.ErrAlreadyExists {
			return &api.CreateClientResp{AlreadyExists: true}, nil
		}
		d.logger.Errorf("api: failed to create client: %v", err)
		return nil, fmt.Errorf("create client: %v", err)
	}

	return &api.CreateClientResp{
		Client: req.Client,
	}, nil
}

func (d dexAPI) DeleteClient(ctx context.Context, req *api.DeleteClientReq) (*api.DeleteClientResp, error) {
	err := d.s.DeleteClient(req.Id)
	if err != nil {
		if err == storage.ErrNotFound {
			return &api.DeleteClientResp{NotFound: true}, nil
		}
		d.logger.Errorf("api: failed to delete client: %v", err)
		return nil, fmt.Errorf("delete client: %v", err)
	}
	return &api.DeleteClientResp{}, nil
}

// checkCost returns an error if the hash provided does not meet minimum cost requirement
func checkCost(hash []byte) error {
	actual, err := bcrypt.Cost(hash)
	if err != nil {
		return fmt.Errorf("parsing bcrypt hash: %v", err)
	}
	if actual < bcrypt.DefaultCost {
		return fmt.Errorf("given hash cost = %d, does not meet minimum cost requirement = %d", actual, bcrypt.DefaultCost)
	}
	return nil
}

func (d dexAPI) CreatePassword(ctx context.Context, req *api.CreatePasswordReq) (*api.CreatePasswordResp, error) {
	if req.Password == nil {
		return nil, errors.New("no password supplied")
	}
	if req.Password.UserId == "" {
		return nil, errors.New("no user ID supplied")
	}
	if req.Password.Hash != nil {
		if err := checkCost(req.Password.Hash); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("no hash of password supplied")
	}

	p := storage.Password{
		Email:    req.Password.Email,
		Hash:     req.Password.Hash,
		Username: req.Password.Username,
		UserID:   req.Password.UserId,
	}
	if err := d.s.CreatePassword(p); err != nil {
		if err == storage.ErrAlreadyExists {
			return &api.CreatePasswordResp{AlreadyExists: true}, nil
		}
		d.logger.Errorf("api: failed to create password: %v", err)
		return nil, fmt.Errorf("create password: %v", err)
	}

	return &api.CreatePasswordResp{}, nil
}

func (d dexAPI) UpdatePassword(ctx context.Context, req *api.UpdatePasswordReq) (*api.UpdatePasswordResp, error) {
	if req.Email == "" {
		return nil, errors.New("no email supplied")
	}
	if req.NewHash == nil && req.NewUsername == "" {
		return nil, errors.New("nothing to update")
	}

	if req.NewHash != nil {
		if err := checkCost(req.NewHash); err != nil {
			return nil, err
		}
	}

	updater := func(old storage.Password) (storage.Password, error) {
		if req.NewHash != nil {
			old.Hash = req.NewHash
		}

		if req.NewUsername != "" {
			old.Username = req.NewUsername
		}

		return old, nil
	}

	if err := d.s.UpdatePassword(req.Email, updater); err != nil {
		if err == storage.ErrNotFound {
			return &api.UpdatePasswordResp{NotFound: true}, nil
		}
		d.logger.Errorf("api: failed to update password: %v", err)
		return nil, fmt.Errorf("update password: %v", err)
	}

	return &api.UpdatePasswordResp{}, nil
}

func (d dexAPI) DeletePassword(ctx context.Context, req *api.DeletePasswordReq) (*api.DeletePasswordResp, error) {
	if req.Email == "" {
		return nil, errors.New("no email supplied")
	}

	err := d.s.DeletePassword(req.Email)
	if err != nil {
		if err == storage.ErrNotFound {
			return &api.DeletePasswordResp{NotFound: true}, nil
		}
		d.logger.Errorf("api: failed to delete password: %v", err)
		return nil, fmt.Errorf("delete password: %v", err)
	}
	return &api.DeletePasswordResp{}, nil

}

func (d dexAPI) GetVersion(ctx context.Context, req *api.VersionReq) (*api.VersionResp, error) {
	return &api.VersionResp{
		Server: version.Version,
		Api:    apiVersion,
	}, nil
}

func (d dexAPI) ListPasswords(ctx context.Context, req *api.ListPasswordReq) (*api.ListPasswordResp, error) {
	passwordList, err := d.s.ListPasswords()
	if err != nil {
		d.logger.Errorf("api: failed to list passwords: %v", err)
		return nil, fmt.Errorf("list passwords: %v", err)
	}

	var passwords []*api.Password
	for _, password := range passwordList {
		p := api.Password{
			Email:    password.Email,
			Username: password.Username,
			UserId:   password.UserID,
		}
		passwords = append(passwords, &p)
	}

	return &api.ListPasswordResp{
		Passwords: passwords,
	}, nil

}

func (d dexAPI) ListRefresh(ctx context.Context, req *api.ListRefreshReq) (*api.ListRefreshResp, error) {
	id := new(internal.IDTokenSubject)
	if err := internal.Unmarshal(req.UserId, id); err != nil {
		d.logger.Errorf("api: failed to unmarshal ID Token subject: %v", err)
		return nil, err
	}

	offlineSessions, err := d.s.GetOfflineSessions(id.UserId, id.ConnId)
	if err != nil {
		d.logger.Errorf("api: failed to list refresh tokens: %v", err)
		return nil, err
	}

	var refreshTokenRefs []*api.RefreshTokenRef
	for _, session := range offlineSessions.Refresh {
		r := api.RefreshTokenRef{
			Id:        session.ID,
			ClientId:  session.ClientID,
			CreatedAt: session.CreatedAt.String(),
			LastUsed:  session.LastUsed.String(),
		}
		refreshTokenRefs = append(refreshTokenRefs, &r)
	}

	return &api.ListRefreshResp{
		RefreshTokens: refreshTokenRefs,
	}, nil
}

func (d dexAPI) RevokeRefresh(ctx context.Context, req *api.RevokeRefreshReq) (*api.RevokeRefreshResp, error) {
	id := new(internal.IDTokenSubject)
	if err := internal.Unmarshal(req.UserId, id); err != nil {
		d.logger.Errorf("api: failed to unmarshal ID Token subject: %v", err)
		return nil, err
	}

	var refreshID string
	updater := func(old storage.OfflineSessions) (storage.OfflineSessions, error) {
		if refreshID = old.Refresh[req.ClientId].ID; refreshID == "" {
			return old, fmt.Errorf("user does not have a refresh token for the client = %s", req.ClientId)
		}

		// Remove entry from Refresh list of the OfflineSession object.
		delete(old.Refresh, req.ClientId)

		return old, nil
	}

	if err := d.s.UpdateOfflineSessions(id.UserId, id.ConnId, updater); err != nil {
		if err == storage.ErrNotFound {
			return &api.RevokeRefreshResp{NotFound: true}, nil
		}
		d.logger.Errorf("api: failed to update offline session object: %v", err)
		return nil, err
	}

	// Delete the refresh token from the storage
	if err := d.s.DeleteRefresh(refreshID); err != nil {
		d.logger.Errorf("failed to delete refresh token: %v", err)
		return nil, err
	}

	return &api.RevokeRefreshResp{}, nil
}
