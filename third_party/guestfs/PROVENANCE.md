# Pinned upstream bindings

`guestfs.go` and `go.mod` are unmodified, generated upstream Go bindings from
libguestfs 1.48.6. The generated API is compatible with the newer native library
on Debian 13, as checked by the native CI builds. The library is dynamically
linked from the destination host.

Source: https://download.libguestfs.org/1.48-stable/libguestfs-1.48.6.tar.gz

Archive SHA256: `0c03cdb2f3460fa492e8423a003e3aa7d51a1311906e01467d1fb90ebf336ac5`

Original paths: `golang/src/libguestfs.org/guestfs/{guestfs.go,go.mod}`.
The copyright and LGPL notices remain in `guestfs.go`; upstream supplies the
LGPL 2.1 text in `COPYING.LIB`. These files are not covered by pvedisk's MIT license.

To update, obtain an upstream release archive, verify its signature and hash,
and replace these files. Recheck the Debian 13 CGO build before raising
the binding version because new symbols may require newer host libraries.
