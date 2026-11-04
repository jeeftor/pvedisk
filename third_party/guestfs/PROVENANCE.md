# Pinned upstream bindings

`guestfs.go` and `go.mod` are unmodified, generated upstream Go bindings from
libguestfs 1.48.6. This version matches the oldest supported native library
(Debian 12). The library is dynamically linked from the destination host.

Source: https://download.libguestfs.org/1.48-stable/libguestfs-1.48.6.tar.gz

Archive SHA256: `0c03cdb2f3460fa492e8423a003e3aa7d51a1311906e01467d1fb90ebf336ac5`

Original paths: `golang/src/libguestfs.org/guestfs/{guestfs.go,go.mod}`.
The copyright and LGPL notices remain in `guestfs.go`; upstream supplies the
LGPL 2.1 text in `COPYING.LIB`. These files are not covered by pvedisk's MIT license.

To update, obtain an upstream release archive, verify its signature and hash,
and replace these files. Recheck both Debian 12 and 13 CGO builds before raising
the binding version because new symbols may require newer host libraries.
