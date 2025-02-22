/*-
 * Copyright 2016 Square Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/crypto/ssh"
)

func (c *context) Enroll(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hostname := vars["hostname"]
	cert, err := c.EnrollHost(hostname, r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, err.Error())
		return
	}
	fmt.Fprintf(w, cert)
}

func (c *context) EnrollHost(hostname string, r *http.Request) (string, error) {
	if !validClient(hostname, r) {
		return "", errors.New("invalid client auth")
	}
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	encodedPubkey := string(data)
	pubkey, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return "", err
	}

	//Update table with host
	rows, err := c.db.Query("SELECT id FROM hostkeys WHERE hostname=?", hostname)
	if err != nil {
		return "", err
	}
	newHost := true
	if rows.Next() {
		newHost = false
	}
	if newHost {
		_, err = c.db.Exec("INSERT INTO hostkeys(hostname, pubkey) VALUES(?,?)", hostname, encodedPubkey)
		if err != nil {
			return "", err
		}
	} else {
		_, err = c.db.Exec("UPDATE hostkeys SET pubkey=? WHERE hostname=?", encodedPubkey, hostname)
		if err != nil {
			return "", err
		}
	}

	// Get id to use for serial number
	rows, err = c.db.Query("SELECT id FROM hostkeys WHERE hostname=?", hostname)
	if err != nil {
		return "", err
	}
	var id uint64
	for rows.Next() {
		err = rows.Scan(&id)
		if err != nil {
			return "", err
		}
	}
	signedCert, err := c.signHost(hostname, uint64(id), pubkey)
	if err != nil {
		return "", err
	}
	certString := base64.StdEncoding.EncodeToString(signedCert.Marshal())
	header := signedCert.Key.Type() + "-cert-v01@openssh.com "
	return header + certString, nil
}

func validClient(hostname string, r *http.Request) bool {
	conn := r.TLS
	if len(conn.VerifiedChains) == 0 {
		return false
	}
	cert := conn.VerifiedChains[0][0]
	return cert.VerifyHostname(hostname) == nil
}

func (c *context) signHost(hostname string, serial uint64, pubkey ssh.PublicKey) (*ssh.Certificate, error) {
	privateKey, err := ioutil.ReadFile(c.conf.SigningKey)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	if err != nil {
		return nil, err
	}
	startTime := time.Now()
	week, err := time.ParseDuration(c.conf.CertDuration)
	if err != nil {
		return nil, err
	}
	endTime := startTime.Add(week)
	principals := []string{hostname}
	if c.conf.StripSuffix != "" && strings.HasSuffix(hostname, c.conf.StripSuffix) {
		principals = append(principals, strings.TrimSuffix(hostname, c.conf.StripSuffix))
	}
	template := ssh.Certificate{
		Nonce:           nonce,
		Key:             pubkey,
		Serial:          serial,
		CertType:        2, //specifies it's a host cert, not user cert
		KeyId:           hostname,
		ValidPrincipals: principals,
		ValidAfter:      (uint64)(startTime.Unix()),
		ValidBefore:     (uint64)(endTime.Unix()),
	}

	template.SignCert(rand.Reader, signer)
	return &template, nil
}
