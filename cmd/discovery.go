package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"io/ioutil"
	"path/filepath"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/yaml"
)

func apiGroupList(rl map[string]*metav1.APIResourceList) (*metav1.APIGroupList, error) {
	result := &metav1.APIGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "APIGroupList",
			APIVersion: "v1",
		},
	}
	for groupVersion := range rl {
		if groupVersion == "v1" {
			continue
		}
		split := strings.Split(groupVersion, "/")
		if len(split) != 2 {
			return nil, fmt.Errorf("groupVErsion %q does not yield exactly two result when slash splitting", groupVersion)
		}

		// TODO: This assumes there is never more than one version, is this safe?
		group, version := split[0], split[1]
		result.Groups = append(result.Groups, metav1.APIGroup{
			Name: group,
			Versions: []metav1.GroupVersionForDiscovery{{
				GroupVersion: groupVersion,
				Version:      version,
			}},
		})
	}

	return result, nil
}

func serializeAPIResourceList(rl map[string]*metav1.APIResourceList) (map[string][]byte, error) {
	result := make(map[string][]byte, len(rl))
	for k, v := range rl {
		serialized, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize resorucelist for group %s: %w", k, err)
		}
		result[k] = serialized
	}

	return result, nil
}

func discover(basePath string) (map[string]*metav1.APIResourceList, error) {
	errs := errorGroup{}
	result := map[string]*metav1.APIResourceList{}
	lock := sync.Mutex{}
	wg := sync.WaitGroup{}

	filepath.WalkDir(basePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs.add(fmt.Errorf("error walking at %s: %w", path, err))
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".yaml") {
			return nil
		}
		wg.Add(1)
		// TODO: Optimize by stopping here if the group and object are already discovered
		// this likely requires to key the map by group and not by groupVersion
		go func() {
			defer wg.Done()
			raw, err := ioutil.ReadFile(path)
			if err != nil {
				errs.add(fmt.Errorf("failed to read file %s: %w", path, err))
				return
			}

			unstructuredList := unstructured.UnstructuredList{}
			if err := yaml.Unmarshal(raw, &unstructuredList); err != nil {
				errs.add(fmt.Errorf("failed to decode %s into an unstructuredList: %w", path, err))
				return
			}
			if len(unstructuredList.Items) < 1 {
				return
			}
			name := strings.TrimSuffix(d.Name(), ".yaml")
			kind := unstructuredList.Items[0].GetKind()
			groupVersion := unstructuredList.Items[0].GetAPIVersion()
			namespaced := strings.Contains(path, "namespaces/")

			lock.Lock()
			defer lock.Unlock()
			if _, hasEntry := result[groupVersion]; !hasEntry {
				result[groupVersion] = &metav1.APIResourceList{
					GroupVersion: groupVersion,
				}
			}
			for _, resource := range result[groupVersion].APIResources {
				// Entry for our resource already exist, nothing to do
				if resource.Name == name {
					return
				}
			}

			result[groupVersion].APIResources = append(result[groupVersion].APIResources, metav1.APIResource{
				// TODO: Shortnames, this will required a hardcoded name -> shortnames mapping
				Name:       name,
				Namespaced: namespaced,
				Kind:       kind,
				Verbs:      []string{"get", "list"},
			})
		}()

		return nil
	})

	wg.Wait()
	return result, utilerrors.NewAggregate(errs.errs)
}

type errorGroup struct {
	errs []error
	lock sync.Mutex
}

func (e *errorGroup) add(err error) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.errs = append(e.errs, err)
}
