package sftpfast

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
)

// validRemoteName rejects anything that is not a single path element, so a
// user-typed name can never redirect an operation elsewhere on the server.
func validRemoteName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid name %q", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("name %q may not contain path separators", name)
	}
	return nil
}

// Remove deletes a remote file, or a directory and everything under it.
func (c *Client) Remove(ctx context.Context, remotePath string) error {
	clean := path.Clean(remotePath)
	if clean == "/" || clean == "." || clean == "" {
		return fmt.Errorf("refusing to delete %q", remotePath)
	}
	// Lstat, never Stat: Stat resolves a symlink, so deleting a link to a
	// directory would recurse into and destroy the TARGET's contents
	// somewhere else on the server. Deleting a link removes the link.
	st, err := c.sftp.Lstat(clean)
	if err != nil {
		return fmt.Errorf("stat %q: %w", clean, err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		if err := c.sftp.Remove(clean); err != nil {
			return fmt.Errorf("delete %q: %w", path.Base(clean), err)
		}
		return nil
	}
	return c.removeTree(ctx, clean, 0)
}

func (c *Client) removeTree(ctx context.Context, dir string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 32 {
		return fmt.Errorf("directory tree deeper than 32 levels at %q", dir)
	}
	entries, err := c.sftp.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read %q: %w", dir, err)
	}
	for _, e := range entries {
		child := path.Join(dir, e.Name())
		// Never descend through a symlink: some servers answer READDIR with
		// stat (not lstat) attributes, which would report a linked directory
		// as a real one and delete the TARGET's contents somewhere else on
		// the server. Lstat asks about the link itself.
		if e.IsDir() {
			link, lerr := c.sftp.Lstat(child)
			if lerr == nil && link.Mode()&os.ModeSymlink != 0 {
				if err := c.sftp.Remove(child); err != nil {
					return fmt.Errorf("delete link %q: %w", child, err)
				}
				continue
			}
			if err := c.removeTree(ctx, child, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := c.sftp.Remove(child); err != nil {
			return fmt.Errorf("delete %q: %w", child, err)
		}
	}
	if err := c.sftp.RemoveDirectory(dir); err != nil {
		return fmt.Errorf("delete folder %q: %w", path.Base(dir), err)
	}
	return nil
}

// RenameEntry renames a remote entry within its directory.
func (c *Client) RenameEntry(remotePath, newName string) error {
	if err := validRemoteName(newName); err != nil {
		return err
	}
	clean := path.Clean(remotePath)
	target := path.Join(path.Dir(clean), newName)
	if _, err := c.sftp.Stat(target); err == nil {
		return fmt.Errorf("%q already exists", newName)
	}
	if err := c.sftp.Rename(clean, target); err != nil {
		return fmt.Errorf("rename to %q: %w", newName, err)
	}
	return nil
}

// MkdirEntry creates a remote directory inside parent.
func (c *Client) MkdirEntry(parent, name string) error {
	if err := validRemoteName(name); err != nil {
		return err
	}
	if err := c.sftp.Mkdir(path.Join(path.Clean(parent), name)); err != nil {
		return fmt.Errorf("create folder %q: %w", name, err)
	}
	return nil
}
