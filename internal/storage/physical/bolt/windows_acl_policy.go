package bolt

// NT file objects map GENERIC_ALL (0x10000000) to FILE_ALL_ACCESS (0x001f01ff)
// when an ACL is installed. This check applies only after the owner, protected
// DACL, sole allow ACE and current-user trustee have all been authenticated.
func windowsFullControlMask(mask uint32) bool {
	return mask == 0x10000000 || mask == 0x001f01ff
}
