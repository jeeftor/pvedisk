package PVE::Tools;
use strict;
use warnings;
sub run_command { my ($cmd, %opts) = @_; $opts{outfunc}->('{"format":"qcow2"}'); }
1;
