// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package drive

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/odeke-em/log"
	drive "google.golang.org/api/drive/v2"
)

type keyValue struct {
	key   string
	value interface{}
}

func (g *Commands) StatById() error {
	return g.statfn("statById", g.rem.FindById)
}

func (g *Commands) Stat() error {
	return g.statfn("stat", g.rem.FindByPath)
}

func (g *Commands) statfn(fname string, fn func(string) (*File, error)) error {
	var err error
	for _, src := range g.opts.Sources {
		f, fErr := fn(src)
		if fErr != nil {
			msg := fmt.Sprintf("%s: %s err: %v\n", fname, src, fErr)
			err = reComposeError(err, msg)
			err = copyErrStatusCode(err, fErr)
			continue
		}

		if g.opts.Md5sum {
			src = f.Name // forces filename if -id is used

			// md5sum with no arguments should do md5sum *
			if f.IsDir && rootLike(g.opts.Path) {
				src = ""
			}
		}

		if fErr = g.stat(src, f, g.opts.Depth); fErr != nil {
			msg := fmt.Sprintf("%s: %s err: %v\n", fname, src, fErr)
			err = reComposeError(err, msg)
			err = copyErrStatusCode(err, fErr)
			continue
		}
	}

	return err
}

func prettyPermission(logf log.Loggerf, perm *drive.Permission) {
	logf("\n*\nName: %v <%s>\n", perm.Name, perm.EmailAddress)
	kvList := []*keyValue{
		&keyValue{"Role", perm.Role},
		&keyValue{"AccountType", perm.Type},
	}

	for _, kv := range kvList {
	logf("%-20s %-30v\n", kv.key, kv.value.(string))
	}
	logf("*\n")

}

func prettyFilePermission(logf log.Loggerf, perm *drive.Permission, file *File) {
	dirType := "file"
	if file.IsDir {
		dirType = "folder"
	}

	kvList := []*keyValue{
		&keyValue{"Role", perm.Role},
		&keyValue{"AccountType", perm.Type},
	}
	logf("%-60v,%-10v,%-25v,%-25v\t\t", file.Name, dirType, perm.Name, perm.EmailAddress)
	for _, kv := range kvList {
		logf(",%-25v", kv.value.(string)) 
	}
	logf("\n")
}

func prettyFileStat(logf log.Loggerf, relToRootPath string, file *File) {
	dirType := "file"
	if file.IsDir {
		dirType = "folder"
	}

	logf("\n\033[92m%s\033[00m\n", relToRootPath)

	kvList := []*keyValue{
		&keyValue{"Filename", file.Name},
		&keyValue{"FileId", file.Id},
		&keyValue{"Bytes", fmt.Sprintf("%v", file.Size)},
		&keyValue{"Size", prettyBytes(file.Size)},
		&keyValue{"QuotaBytesUsed", fmt.Sprintf("%v", file.QuotaBytesUsed)},
		&keyValue{"DirType", dirType},
		&keyValue{"VersionNumber", fmt.Sprintf("%v", file.Version)},
		&keyValue{"MimeType", file.MimeType},
		&keyValue{"Etag", file.Etag},
		&keyValue{"ModTime", fmt.Sprintf("%v", file.ModTime)},
		&keyValue{"LastViewedByMe", fmt.Sprintf("%v", file.LastViewedByMeTime)},
		&keyValue{"Shared", fmt.Sprintf("%v", file.Shared)},
		&keyValue{"Owners", sepJoin(" & ", file.OwnerNames...)},
		&keyValue{"LastModifyingUsername", file.LastModifyingUsername},
	}

	if file.Description != "" {
		kvList = append(kvList, &keyValue{"Description", fmt.Sprintf("%q", file.Description)})
	}

	if file.Name != file.OriginalFilename {
		kvList = append(kvList, &keyValue{"OriginalFilename", file.OriginalFilename})
	}

	if !file.IsDir {
		kvList = append(kvList, &keyValue{"Md5Checksum", file.Md5Checksum})

		// By default, folders are non-copyable, but drive implements recursively copying folders
		kvList = append(kvList, &keyValue{"Copyable", fmt.Sprintf("%v", file.Copyable)})
	}

	if file.Labels != nil {
		kvList = append(kvList,
			&keyValue{"Starred", fmt.Sprintf("%v", file.Labels.Starred)},
			&keyValue{"Viewed", fmt.Sprintf("%v", file.Labels.Viewed)},
			&keyValue{"Trashed", fmt.Sprintf("%v", file.Labels.Trashed)},
			&keyValue{"ViewersCanDownload", fmt.Sprintf("%v", file.Labels.Restricted)},
		)
	}

	for _, kv := range kvList {
		logf("%-25s %-30v\n", kv.key, kv.value.(string))
	}
}
	
var firstLine = true

func (g *Commands) stat(relToRootPath string, file *File, depth int) error {
	if g.opts.Md5sum {
		if file.Md5Checksum != "" {
			g.log.Logf("%32s  %s\n", file.Md5Checksum, strings.TrimPrefix(relToRootPath, "/"))
		}
	} else {
		perms, permErr := g.rem.listPermissions(file.Id)
		if g.opts.CsvOutput {
			if firstLine {
				g.log.Logf("File Name, Type, Name, Email, Role, AccountType\n")
				firstLine = false
			}
			for _, perm := range perms {
				prettyFilePermission(g.log.Logf, perm, file)
			}
		} else {
			prettyFileStat(g.log.Logf, relToRootPath, file)
			if permErr != nil {
				return permErr
			}

			for _, perm := range perms {
				prettyPermission(g.log.Logf, perm)
			}
		}

	}

	if depth == 0 {
		return nil
	}

	if !file.IsDir {
		return nil
	}

	if depth >= 1 {
		depth -= 1
	}

	var remoteChildren []*File

	pagePair := g.rem.FindByParentId(file.Id, g.opts.Hidden)
	errsChan := pagePair.errsChan
	childrenChan := pagePair.filesChan

	working := true
	for working {
		select {
		case err := <-errsChan:
			if err != nil {
				return err
			}
		case child, stillHasContent := <-childrenChan:
			if !stillHasContent {
				working = false
				break
			}
			remoteChildren = append(remoteChildren, child)
		}
	}

	if g.opts.Md5sum {
		g.sort(remoteChildren, Md5Key, NameKey)
	}

	for _, child := range remoteChildren {
		g.stat(filepath.Clean(relToRootPath+"/"+child.Name), child, depth)
	}

	return nil
}
