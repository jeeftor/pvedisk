package PVE::Storage;
use strict;
use warnings;
sub config { return {}; }
sub path { return ($ENV{PVEDISK_TEST_DISK}, 400, 'images'); }
1;
