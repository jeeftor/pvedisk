package PVE::QemuConfig;
use strict;
use warnings;
use JSON::PP;
sub lock_config { die 'listing must not acquire locks' if $ENV{PVEDISK_TEST_LIST}; my ($class, $vmid, $callback) = @_; $callback->(); }
sub test_configs {
    open(my $f, '<', $ENV{PVEDISK_TEST_CONFIG}) or die $!;
    local $/; return decode_json(<$f>);
}
sub load_config {
    my ($class, $vmid) = @_;
    my $conf = test_configs();
    return $ENV{PVEDISK_TEST_LIST} ? $conf->{$vmid} : $conf;
}
sub config_file { return $ENV{PVEDISK_TEST_CONFIG}; }
sub write_config {
    die 'listing must not write configuration' if $ENV{PVEDISK_TEST_LIST};
    my ($class, $vmid, $conf) = @_;
    open(my $f, '>', $ENV{PVEDISK_TEST_CONFIG}) or die $!;
    print $f encode_json($conf); close($f) or die $!;
}
sub check_lock { die "VM is locked\n" if $_[1]->{lock}; }
sub parse_volume { die 'listing must not inspect disks' if $ENV{PVEDISK_TEST_LIST}; return $_[2]; }
1;
