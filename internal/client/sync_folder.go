package client

import "errors"

// errSyncFolderCancelled reports that the person closed the folder picker.
var errSyncFolderCancelled = errors.New("client: sync folder selection canceled")
