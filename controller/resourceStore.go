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
	"reflect"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/coreos/etcd/client"
	"github.com/satori/go.uuid"
	"golang.org/x/net/context"
)

type (
	resourceStore struct {
		*fileStore
		client.KeysAPI
		serializer
	}
)

func makeResourceStore(fs *fileStore, ks client.KeysAPI, s serializer) *resourceStore {
	return &resourceStore{fileStore: fs, KeysAPI: ks, serializer: s}
}

func getEtcdKeyAPI(etcdUrls []string) client.KeysAPI {
	cfg := client.Config{
		Endpoints: etcdUrls,
		Transport: client.DefaultTransport,
		// set timeout per request to fail fast when the target endpoint is unavailable
		HeaderTimeoutPerRequest: time.Second,
	}
	c, err := client.New(cfg)
	if err != nil {
		log.Fatalf("failed to connect to etcd: %v", err)
	}
	return client.NewKeysAPI(c)
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

func (rs *resourceStore) create(r resource) error {
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
	return err
}

func (rs *resourceStore) read(rkey string, res resource) error {
	typName, err := getTypeName(res)
	if err != nil {
		return err
	}
	key := typName + "/" + rkey

	resp, err := rs.KeysAPI.Get(context.Background(), key, nil)
	if err != nil {
		return err
	}
	return rs.serializer.deserialize([]byte(resp.Node.Value), res)
}

func (rs *resourceStore) update(r resource) error {
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
	return err
}

func (rs *resourceStore) delete(typename, rkey string) error {
	key := typename + "/" + rkey
	_, err := rs.KeysAPI.Delete(context.Background(), key, nil) // ignore response
	return err
}

func (rs *resourceStore) getAll(key string) ([]string, error) {
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Recursive: true})
	if err != nil {
		return nil, err
	}

	res := make([]string, 0, len(resp.Node.Nodes))
	for _, n := range resp.Node.Nodes {
		res = append(res, n.Value)
	}
	return res, nil
}

func (rs *resourceStore) writeFile(parentKey string, contents []byte) (string, string, error) {
	uid := uuid.NewV4().String()

	err := rs.fileStore.write(uid, contents)
	if err != nil {
		return "", "", err
	}

	parentKey = "file/" + parentKey
	resp, err := rs.KeysAPI.CreateInOrder(context.Background(), parentKey, uid, nil)
	if err != nil {
		_ = rs.fileStore.delete(uid)
		return "", "", err
	}

	return resp.Node.Key, uid, nil
}

func (rs *resourceStore) readFile(key string, uid *string) ([]byte, error) {
	key = "file/" + key
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Sort: true})
	if err != nil {
		return nil, err
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

	contents, err := rs.fileStore.read(*uid)
	return contents, err
}

func (rs *resourceStore) deleteFile(key string, uid string) error {
	key = "file/" + key
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Sort: true})
	if err != nil {
		return err
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

	err = rs.fileStore.delete(node.Value)
	if err != nil {
		return err
	}

	_, err = rs.KeysAPI.Delete(context.Background(), node.Key, nil)
	if err != nil {
		return err
	}

	if len(resp.Node.Nodes) == 1 {
		_, err = rs.KeysAPI.Delete(context.Background(), key, &client.DeleteOptions{Dir: true})
		return err
	}
	return nil
}

func (rs *resourceStore) deleteAllFiles(key string) error {
	key = "file/" + key
	resp, err := rs.KeysAPI.Get(context.Background(), key, &client.GetOptions{Sort: true})
	if err != nil {
		return err
	}
	for _, u := range resp.Node.Nodes {
		err = rs.fileStore.delete(u.Value)
		if err != nil {
			return err
		}

		_, err = rs.KeysAPI.Delete(context.Background(), u.Key, nil)
		if err != nil {
			return err
		}
	}
	_, err = rs.KeysAPI.Delete(context.Background(), key, &client.DeleteOptions{Dir: true})
	return err
}
