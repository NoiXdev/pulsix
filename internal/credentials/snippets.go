package credentials

// AuthorizedKey is the public key line with its Pulsix comment, exactly as it
// goes into authorized_keys, or "" for a credential without a key. The
// comment is composed on read, so renaming a credential updates it.
func AuthorizedKey(c Credential, orgSlug string) string {
	if c.PublicKey == "" {
		return ""
	}
	return c.PublicKey + " pulsix:" + orgSlug + ":" + c.Name
}

// Snippets are ready-to-paste ways to install an SSH key credential's public
// key on a host (spec §5).
type Snippets struct {
	AuthorizedKeysCommand string
	CloudInit             string
	Terraform             string
}

// SnippetsFor returns the snippets for an SSH key credential and the zero
// value for every other kind. Names, usernames and keys are restricted to
// characters that need no quoting in these snippets.
func SnippetsFor(c Credential, orgSlug string) Snippets {
	if c.Kind != KindSSHKey || c.PublicKey == "" {
		return Snippets{}
	}
	key := AuthorizedKey(c, orgSlug)
	user := c.Username
	if user == "" {
		user = DefaultLoginUser
	}
	return Snippets{
		AuthorizedKeysCommand: "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '" + key + "' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys",
		CloudInit: "#cloud-config\nusers:\n  - name: " + user + "\n    shell: /bin/bash\n    sudo: \"ALL=(ALL) NOPASSWD:ALL\"\n" +
			"    ssh_authorized_keys:\n      - " + key + "\n",
		Terraform: "# Public key of the Pulsix credential \"" + c.Name + "\"\nlocals {\n  pulsix_ssh_public_key = \"" + key + "\"\n}\n",
	}
}
