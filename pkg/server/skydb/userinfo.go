// Copyright 2015-present Oursky Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package skydb

import (
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/skygeario/skygear-server/pkg/server/utils"
	"github.com/skygeario/skygear-server/pkg/server/uuid"
)

// AuthInfo represents the dictionary of authenticated principal ID => authData.
//
// For example, a UserInfo connected with a Facebook account might
// look like this:
//
//   {
//     "com.facebook:46709394": {
//       "accessToken": "someAccessToken",
//       "expiredAt": "2015-02-26T20:05:48",
//       "facebookID": "46709394"
//     }
//   }
//
// It is assumed that the Facebook AuthProvider has "com.facebook" as
// provider name and "46709394" as the authenticated Facebook account ID.
type AuthInfo map[string]map[string]interface{}

// UserInfo contains a user's information for authentication purpose
type UserInfo struct {
	ID              string     `json:"_id"`
	Username        string     `json:"username,omitempty"`
	Email           string     `json:"email,omitempty"`
	HashedPassword  []byte     `json:"password,omitempty"`
	Roles           []string   `json:"roles,omitempty"`
	Auth            AuthInfo   `json:"auth,omitempty"` // auth data for alternative methods
	TokenValidSince *time.Time `json:"token_valid_since,omitempty"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
}

// NewUserInfo returns a new UserInfo with specified username, email and
// password. An UUID4 ID will be generated by the system as unique identifier
func NewUserInfo(username string, email string, password string) UserInfo {
	id := uuid.New()

	info := UserInfo{
		ID:       id,
		Username: username,
		Email:    email,
	}
	info.SetPassword(password)

	return info
}

// NewAnonymousUserInfo returns an anonymous UserInfo, which has
// no Email and Password.
func NewAnonymousUserInfo() UserInfo {
	return UserInfo{
		ID: uuid.New(),
	}
}

// NewProvidedAuthUserInfo returns an UserInfo provided by a AuthProvider,
// which has no Email and Password.
func NewProvidedAuthUserInfo(principalID string, authData map[string]interface{}) UserInfo {
	return UserInfo{
		ID: uuid.New(),
		Auth: AuthInfo(map[string]map[string]interface{}{
			principalID: authData,
		}),
	}
}

// SetPassword sets the HashedPassword with the password specified
func (info *UserInfo) SetPassword(password string) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		panic("userinfo: Failed to hash password")
	}

	info.HashedPassword = hashedPassword

	// Changing the password will also update the time before which issued
	// access token should be invalidated.
	timeNow := time.Now().UTC()
	info.TokenValidSince = &timeNow
}

// IsSamePassword determines whether the specified password is the same
// password as where the HashedPassword is generated from
func (info UserInfo) IsSamePassword(password string) bool {
	return bcrypt.CompareHashAndPassword(info.HashedPassword, []byte(password)) == nil
}

// SetProvidedAuthData sets the auth data to the specified principal.
func (info *UserInfo) SetProvidedAuthData(principalID string, authData map[string]interface{}) {
	if info.Auth == nil {
		info.Auth = make(map[string]map[string]interface{})
	}
	info.Auth[principalID] = authData
}

// HasAnyRoles return true if userinfo belongs to one of the supplied roles
func (info *UserInfo) HasAnyRoles(roles []string) bool {
	return utils.StringSliceContainAny(info.Roles, roles)
}

// HasAllRoles return true if userinfo has all roles supplied
func (info *UserInfo) HasAllRoles(roles []string) bool {
	return utils.StringSliceContainAll(info.Roles, roles)
}

// GetProvidedAuthData gets the auth data for the specified principal.
func (info *UserInfo) GetProvidedAuthData(principalID string) map[string]interface{} {
	if info.Auth == nil {
		return nil
	}
	value, _ := info.Auth[principalID]
	return value
}

// RemoveProvidedAuthData remove the auth data for the specified principal.
func (info *UserInfo) RemoveProvidedAuthData(principalID string) {
	if info.Auth != nil {
		delete(info.Auth, principalID)
	}
}
