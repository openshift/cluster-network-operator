package auth

import (
	"io/ioutil"
	"os"

	"github.com/mitchellh/go-homedir"
)

// This is copied directly from Terraform in order to remove a single legacy
// vendor dependency.
// https://github.com/hashicorp/terraform/tree/master/helper/pathorcontents
func pathOrContents(poc string) (string, bool, error) {
	if len(poc) == 0 {
		return poc, false, nil
	}

	path := poc
	if path[0] == '~' {
		var err error
		path, err = homedir.Expand(path)
		if err != nil {
			return path, true, err
		}
	}

	if _, err := os.Stat(path); err == nil {
		contents, err := ioutil.ReadFile(path)
		if err != nil {
			return string(contents), true, err
		}
		return string(contents), true, nil
	}

	return poc, false, nil
}
