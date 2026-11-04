package PVE::Storage;
use strict;
use warnings;
sub config { return {}; }
sub path { die 'listing must not resolve disks' if $ENV{PVEDISK_TEST_LIST}; return ($ENV{PVEDISK_TEST_DISK}, 400, 'images'); }
1;
