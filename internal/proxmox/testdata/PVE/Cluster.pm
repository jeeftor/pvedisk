package PVE::Cluster;
use strict;
use warnings;
sub check_cfs_is_mounted { die "pmxcfs not mounted\n" if $ENV{PVEDISK_TEST_CFS_UNMOUNTED}; return 1; }
sub check_cfs_quorum { die "no quorum\n" if $ENV{PVEDISK_TEST_NO_QUORUM}; return 1; }
my $updates = 0;
our $refreshed = 0;
sub cfs_update {
    die "cluster refresh failed\n" if $ENV{PVEDISK_TEST_CFS_ERROR};
    die "cluster refresh must fail closed\n" if !$_[0];
    $refreshed = 1;
    if ($ENV{PVEDISK_TEST_RECREATE} && ++$updates == 1) {
        open(my $f, '>', $ENV{PVEDISK_TEST_CONFIG}) or die $!;
        print $f '{"name":"recreated","lock":"backup"}'; close($f) or die $!;
    }
}
sub get_vmlist {
    return {} if $ENV{PVEDISK_TEST_BAD_INVENTORY};
    return {version => 1} if $ENV{PVEDISK_TEST_EMPTY_CLUSTER};
    return {version => 1, ids => $ENV{PVEDISK_TEST_REMOTE} ? {400 => {node => 'remote', type => 'qemu'}} : {}};
}
1;
