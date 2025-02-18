// Copyright (c) 2015-2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// Package ldap defines the LDAP configuration object and methods used by the
// MinIO server.
package ldap

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	ldap "github.com/go-ldap/ldap/v3"
)

const (
	dnDelimiter = ";"
)

// Config contains configuration to connect to an LDAP server.
type Config struct {
	Enabled bool

	// E.g. "ldap.minio.io:636"
	ServerAddr     string
	TLSSkipVerify  bool // allows skipping TLS verification
	ServerInsecure bool // allows plain text connection to LDAP server
	ServerStartTLS bool // allows using StartTLS connection to LDAP server
	RootCAs        *x509.CertPool

	// Lookup bind LDAP service account
	LookupBindDN       string
	LookupBindPassword string

	// User DN search parameters
	UserDNSearchBaseDistName  string
	UserDNSearchBaseDistNames []string
	UserDNSearchFilter        string

	// Group search parameters
	GroupSearchBaseDistName  string
	GroupSearchBaseDistNames []string
	GroupSearchFilter        string
}

// Clone creates a copy of the config.
func (l *Config) Clone() (cloned Config) {
	cloned = *l
	return cloned
}

// Connect connect to ldap server.
func (l *Config) Connect() (ldapConn *ldap.Conn, err error) {
	if l == nil || !l.Enabled {
		return nil, errors.New("LDAP is not configured")
	}

	_, _, err = net.SplitHostPort(l.ServerAddr)
	if err != nil {
		// User default LDAP port if none specified "636"
		l.ServerAddr = net.JoinHostPort(l.ServerAddr, "636")
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: l.TLSSkipVerify,
		RootCAs:            l.RootCAs,
	}

	if l.ServerInsecure {
		ldapConn, err = ldap.Dial("tcp", l.ServerAddr)
	} else {
		if l.ServerStartTLS {
			ldapConn, err = ldap.Dial("tcp", l.ServerAddr)
		} else {
			ldapConn, err = ldap.DialTLS("tcp", l.ServerAddr, tlsConfig)
		}
	}

	if ldapConn != nil {
		ldapConn.SetTimeout(30 * time.Second) // Change default timeout to 30 seconds.
		if l.ServerStartTLS {
			err = ldapConn.StartTLS(tlsConfig)
		}
	}

	return ldapConn, err
}

// LookupBind connects to LDAP server using the bind user credentials.
func (l *Config) LookupBind(conn *ldap.Conn) error {
	var err error
	if l.LookupBindPassword == "" {
		err = conn.UnauthenticatedBind(l.LookupBindDN)
	} else {
		err = conn.Bind(l.LookupBindDN, l.LookupBindPassword)
	}
	if ldap.IsErrorWithCode(err, 49) {
		return fmt.Errorf("LDAP Lookup Bind user invalid credentials error: %w", err)
	}
	return err
}

// LookupUserDN searches for the DN of the user given their username. conn is
// assumed to be using the lookup bind service account. It is required that the
// search result in at most one result.
func (l *Config) LookupUserDN(conn *ldap.Conn, username string) (string, error) {
	filter := strings.ReplaceAll(l.UserDNSearchFilter, "%s", ldap.EscapeFilter(username))
	var foundDistNames []string
	for _, userSearchBase := range l.UserDNSearchBaseDistNames {
		searchRequest := ldap.NewSearchRequest(
			userSearchBase,
			ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
			filter,
			[]string{}, // only need DN, so no pass no attributes here
			nil,
		)

		searchResult, err := conn.Search(searchRequest)
		if err != nil {
			return "", err
		}

		for _, entry := range searchResult.Entries {
			foundDistNames = append(foundDistNames, entry.DN)
		}
	}
	if len(foundDistNames) == 0 {
		return "", fmt.Errorf("User DN for %s not found", username)
	}
	if len(foundDistNames) != 1 {
		return "", fmt.Errorf("Multiple DNs for %s found - please fix the search filter", username)
	}
	return foundDistNames[0], nil
}

// SearchForUserGroups finds the groups of the user.
func (l *Config) SearchForUserGroups(conn *ldap.Conn, username, bindDN string) ([]string, error) {
	// User groups lookup.
	var groups []string
	if l.GroupSearchFilter != "" {
		for _, groupSearchBase := range l.GroupSearchBaseDistNames {
			filter := strings.ReplaceAll(l.GroupSearchFilter, "%s", ldap.EscapeFilter(username))
			filter = strings.ReplaceAll(filter, "%d", ldap.EscapeFilter(bindDN))
			searchRequest := ldap.NewSearchRequest(
				groupSearchBase,
				ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
				filter,
				nil,
				nil,
			)

			var newGroups []string
			newGroups, err := getGroups(conn, searchRequest)
			if err != nil {
				errRet := fmt.Errorf("Error finding groups of %s: %w", bindDN, err)
				return nil, errRet
			}

			groups = append(groups, newGroups...)
		}
	}

	return groups, nil
}

func getGroups(conn *ldap.Conn, sreq *ldap.SearchRequest) ([]string, error) {
	var groups []string
	sres, err := conn.Search(sreq)
	if err != nil {
		// Check if there is no matching result and return empty slice.
		// Ref: https://ldap.com/ldap-result-code-reference/
		if ldap.IsErrorWithCode(err, 32) {
			return nil, nil
		}
		return nil, err
	}
	for _, entry := range sres.Entries {
		// We only queried one attribute,
		// so we only look up the first one.
		groups = append(groups, entry.DN)
	}
	return groups, nil
}
