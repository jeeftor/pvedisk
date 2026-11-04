package PVE::QemuConfig;
use strict;
use warnings;
use JSON::PP;
sub lock_config { my ($class, $vmid, $callback) = @_; $callback->(); }
sub load_config {
    open(my $f, '<', $ENV{PVEDISK_TEST_CONFIG}) or die $!;
    local $/; return decode_json(<$f>);
}
sub write_config {
    my ($class, $vmid, $conf) = @_;
    open(my $f, '>', $ENV{PVEDISK_TEST_CONFIG}) or die $!;
    print $f encode_json($conf); close($f) or die $!;
}
sub check_lock { die "VM is locked\n" if $_[1]->{lock}; }
sub parse_volume { return $_[2]; }
1;
