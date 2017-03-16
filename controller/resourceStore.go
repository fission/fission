/*
Copyright 2016 The Fission Authors.

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
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/coreos/etcd/client"
	"github.com/satori/go.uuid"
	"golang.org/x/net/context"

	"github.com/fission/fission"
)

type (
	ResourceStore struct {
		*FileStore
		client.KeysAPI
		serializer
	}
)

func MakeResourceStore(fs *FileStore, etcdUrls []string) (*ResourceStore, error) {
	ks, err := getEtcdKeyAPI(etcdUrls)
	if err != nil {
		return nil, err
	}
	s := JsonSerializer{}
	return &ResourceStore{FileStore: fs, KeysAPI: ks, serializer: s}, nil
}

func getEtcdKeyAPI(etcdUrls []string) (client.KeysAPI, error) {
	cfg := client.Config{
		Endpoints: etcdUrls,
		Transport: client.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
	c, err := client.New(cfg)
	if err != nil {
		log.Printf("failed to connect to etcd: %v", err)
		return nil, err
	}
	return client.NewKeysAPI(c), nil
}

func getTypeName(r resource) (string, error) {
	typ := reflect.TypeOf(r)
	if typ.Kind().String() == "ptr" {
		typ = typ.Elem()
	}
	typName := typ.Name()
	if len(typName) == 0 {
		return "", errors.New("Failed to get type")
	}
	return typName, nil
}

func getKey(r resource) (string, error) {
	typName, err := getTypeName(r)
	if err != nil {
		return "", err
	}
	rkey := r.Key()
	return (typName + "/" + rkey), nil
}

func (rs *ResourceStore) create(r resource) error {
	key, err := getKey(r)
	if err != nil {
		return err
	}

	serialized, err := rs.serializer.serialize(r)
	if err != nil {
		return err
	}

	_, err = rs.KeysAPI.Set(context.Background(), key, string(serialized),
		&client.SetOptions{PrevExist: client.PrevNoExist})
	return handleEtcdErrorForResource(err, r)
}

func (rs *ResourceStore) read(rkey string, res resource) error {
	typName, err := getTypeName(res)
	if err != nil {
		return err
	}
	key := typName + "/" + rkey

	resp, err := rs.KeysAPI.Get(context.Background(), key, nil)
	if err != nil {
		return handleEtcdError(err, typName, rkey)
	}
	return rs.serializer.deserialize([]byte(resp.Node.Value), res)
}

func (rs *ResourceStore) update(r resource) error {
	key, err := getKey(r)
	if err != nil {
		return err
	}

	serialized, err := rs.serializer.serialize(r)
	if err != nil {
		return err
	}

	_, err = rs.KeysAPI.Set(context.Background(), key, string(serialized),
		&client.SetOptions{PrevExist: client.PrevExist})
	return handleEtcdErrorForResource(err, r)
}

func (rs *ResourceStore) delete(typename, rkey string) error {
	key := typename + "/" + rkey
	_, err := rs.KeysAPI.Delete(context.Background(), key, nil) // ignore response
	return handleEtcdError(err, typename, rkey)
}

// getAll finds all entries under key.  If none or found or key
// doesn't exist, returns an empty slice.
func (rs *ResourceStore) getAll(key string) ([]string, error) {
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Recursive: true, Sort: true})
	if err != nil {
		if client.IsKeyNotFound(err) {
			return []string{}, nil
		}
		return nil, handleEtcdError(err, "", key)
	}

	res := make([]string, 0, len(resp.Node.Nodes))
	for _, n := range resp.Node.Nodes {
		res = append(res, n.Value)
	}
	return res, nil
}

func (rs *ResourceStore) writeFile(parentKey string, contents []byte) (string, string, error) {
	uid := uuid.NewV4().String()

	err := rs.FileStore.write(uid, contents)
	if err != nil {
		return "", "", err
	}

	parentKey = "file/" + parentKey
	resp, err := rs.KeysAPI.CreateInOrder(context.Background(), parentKey, uid, nil)
	if err != nil {
		_ = rs.FileStore.delete(uid)
		return "", "", handleEtcdError(err, "file", parentKey)
	}

	return resp.Node.Key, uid, nil
}

func (rs *ResourceStore) readFile(key string, uid *string) ([]byte, error) {
	key = "file/" + key
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Sort: true})
	if err != nil {
		return nil, handleEtcdError(err, "file", key)
	}

	if uid == nil {
		// get latest
		n := resp.Node.Nodes
		uid = &n[len(n)-1].Value
	} else {
		// validate uid is in the list
		found := false
		for _, u := range resp.Node.Nodes {
			if *uid == u.Value {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("Invalid UID " + *uid)
		}
	}

	contents, err := rs.FileStore.read(*uid)
	return contents, err
}

func (rs *ResourceStore) deleteFile(key string, uid string) error {
	key = "file/" + key
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Sort: true})
	if err != nil {
		return handleEtcdError(err, "file", key)
	}

	var node *client.Node
	for _, u := range resp.Node.Nodes {
		if u.Value == uid {
			node = u
		}
	}
	if node == nil {
		log.WithFields(log.Fields{"key": key, "uid": uid}).Error("unreferenced file")
		return errors.New("won't delete unreferenced file")
	}

	err = rs.FileStore.delete(node.Value)
	if err != nil {
		return err
	}

	_, err = rs.KeysAPI.Delete(context.Background(), node.Key, nil)
	if err != nil {
		return handleEtcdError(err, "", node.Key)
	}

	if len(resp.Node.Nodes) == 1 {
		_, err = rs.KeysAPI.Delete(context.Background(), key, &client.DeleteOptions{Dir: true})
		return handleEtcdError(err, "file", key)
	}
	return nil
}

func (rs *ResourceStore) deleteAllFiles(key string) error {
	key = "file/" + key
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Sort: true})
	if err != nil {
		return handleEtcdError(err, "file", key)
	}
	for _, u := range resp.Node.Nodes {
		err = rs.FileStore.delete(u.Value)
		if err != nil {
			return err
		}

		_, err = rs.KeysAPI.Delete(context.Background(), u.Key, nil)
		if err != nil {
			return handleEtcdError(err, "", u.Key)
		}
	}
	_, err = rs.KeysAPI.Delete(context.Background(), key, &client.DeleteOptions{Dir: true})
	return handleEtcdError(err, "file", key)
}

func handleEtcdErrorForResource(e error, r resource) error {
	resourceType, _ := getTypeName(r)
	return handleEtcdError(e, resourceType, r.Key())
}

func handleEtcdError(e error, resourceType string, resourceKey string) error {
	ee, ok := e.(client.Error)
	if !ok {
		return e
	}
	code := fission.ErrorInternal
	msg := ee.Error()

	if len(resourceType) > 0 {
		resourceType = strings.ToLower(resourceType) + " "
	}

	//TODO: handle any other etcd error codes we care about
	switch ee.Code {
	case client.ErrorCodeNodeExist:
		code = fission.ErrorNameExists
		msg = fmt.Sprintf("%s'%s' already exists", resourceType, resourceKey)
	case client.ErrorCodeKeyNotFound:
		code = fission.ErrorNotFound
		msg = fmt.Sprintf("%s'%s' does not exist", resourceType, resourceKey)
	}
	return fission.MakeError(code, msg)
}
