package PVE::QemuServer;
use strict;
use warnings;
sub config_list {
    die 'listing must refresh cluster inventory' if !$PVE::Cluster::refreshed;
    my $configs = PVE::QemuConfig::test_configs();
    return {map { $_ => {exists => 1} } grep { !$configs->{$_}->{test_remote} } keys %$configs};
}
sub vmstatus {
    my ($vmid, $full) = @_;
    die 'full monitor status was not requested' if !$full;
    if (!defined($vmid)) {
        my $configs = PVE::QemuConfig::test_configs();
        return {map { $_ => {
            name => $configs->{$_}->{name},
            status => $configs->{$_}->{test_status} // ($configs->{$_}->{test_running} ? 'running' : 'stopped'),
            qmpstatus => $configs->{$_}->{test_qmp} // ($configs->{$_}->{test_running} ? 'running' : 'stopped'),
        } } keys %$configs};
    }
    my $conf = PVE::QemuConfig->load_config($vmid);
    return {$vmid => {status => $conf->{test_running} ? 'running' : 'stopped', qmpstatus => $conf->{test_qmp} // 'stopped'}};
}
sub check_running {
    return $ENV{PVEDISK_TEST_RUNNING} if $_[1] && !-e $ENV{PVEDISK_TEST_CONFIG};
    return PVE::QemuConfig->load_config($_[0])->{test_running};
}
1;
