package PVE::Tools;
use strict;
use warnings;
sub run_command { die 'listing must not start subprocesses' if $ENV{PVEDISK_TEST_LIST}; my ($cmd, %opts) = @_; $opts{outfunc}->('{"format":"qcow2"}'); }
1;
