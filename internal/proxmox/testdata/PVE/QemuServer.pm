package PVE::QemuServer;
use strict;
use warnings;
sub vmstatus {
    my ($vmid, $full) = @_;
    die 'full monitor status was not requested' if !$full;
    my $conf = PVE::QemuConfig->load_config($vmid);
    return {$vmid => {status => $conf->{test_running} ? 'running' : 'stopped', qmpstatus => $conf->{test_qmp} // 'stopped'}};
}
sub check_running { return PVE::QemuConfig->load_config($_[0])->{test_running}; }
1;
