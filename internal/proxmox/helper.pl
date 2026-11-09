# SPDX-License-Identifier: MIT
# Uses the host's installed Proxmox modules; it does not copy their implementation.
use strict;
use warnings;
use JSON::PP;
use Fcntl qw(O_WRONLY O_CREAT O_EXCL O_RDONLY O_NOFOLLOW);
use IO::Handle;
use Digest::SHA qw(sha256_hex);
use PVE::Cluster;
use PVE::QemuConfig;
use PVE::QemuServer;
use PVE::Storage;
use PVE::HA::Config;
use PVE::Tools;

die "root required\n" if $> != 0;
my ($operation, $vmid, $token) = @ARGV;
die "invalid operation\n" if !defined($operation) || $operation !~ /\A(?:inspect|stopped|acquire|release|doctor|list)\z/;
if ($operation eq 'doctor') { print "[]\n"; exit 0; }
if ($operation eq 'list') {
    # config_list restricts the cluster inventory to local QEMU VMs. Full status
    # asks QMP whether a running process is actually paused or suspended.
    PVE::Cluster::cfs_update(1);
    my $local = PVE::QemuServer::config_list();
    my $statuses = PVE::QemuServer::vmstatus(undef, 1);
    die "unsupported local VM inventory\n" if ref($local) ne 'HASH' || ref($statuses) ne 'HASH';
    my @vms;
    for my $id (sort { $a <=> $b } keys %$local) {
        die "invalid local VM ID\n" if $id !~ /\A[1-9][0-9]{2,8}\z/;
        my $status = $statuses->{$id};
        die "cannot determine status of VM $id\n" if ref($status) ne 'HASH' || ($status->{status} // '') !~ /\A(?:running|stopped)\z/;
        my $conf = PVE::QemuConfig->load_config($id);
        my @reasons;
        push @reasons, "Proxmox lock: $conf->{lock}" if $conf->{lock};
        push @reasons, 'template' if $conf->{template};
        push @reasons, 'suspended state' if $conf->{vmstate};
        push @reasons, 'pending configuration changes' if keys %{$conf->{pending} // {}};
        push @reasons, 'HA-managed' if PVE::HA::Config::vm_is_ha_managed($id);
        my $state = $status->{status};
        my $qmp = $status->{qmpstatus} // '';
        if ($state eq 'running') {
            if ($qmp eq '') { push @reasons, 'monitor status unavailable'; }
            elsif ($qmp ne 'running') { $state = $qmp; push @reasons, "VM is $qmp"; }
        }
        push @vms, {
            id => int($id),
            name => $conf->{name} // $status->{name} // "VM $id",
            status => $state,
            unavailable_reason => join('; ', @reasons),
        };
    }
    print JSON::PP->new->canonical->encode(\@vms), "\n";
    exit 0;
}
die "invalid VM ID\n" if !defined($vmid) || $vmid !~ /\A[1-9][0-9]{2,8}\z/;
die "invalid token\n" if $operation ne 'inspect' && $operation ne 'stopped' && (!defined($token) || $token !~ /\A[A-Za-z0-9-]{16,128}\z/);
my $json = JSON::PP->new->canonical;
my $owner_dir = '/var/lib/pvedisk/locks';
my $owner_path = "$owner_dir/$vmid.json";

sub config_fingerprint {
    my ($conf) = @_;
    my %copy = %$conf;
    delete $copy{digest};
    return sha256_hex($json->encode(\%copy));
}

sub validate_config {
    my ($conf) = @_;
    PVE::QemuConfig->check_lock($conf);
    die "VM is a template\n" if $conf->{template};
    die "VM has suspended state\n" if $conf->{vmstate};
    die "VM has pending configuration changes\n" if keys %{$conf->{pending} // {}};
    die "VM is HA-managed\n" if PVE::HA::Config::vm_is_ha_managed($vmid);
    my $status = PVE::QemuServer::vmstatus($vmid, 1)->{$vmid};
    die "cannot determine VM status\n" if !$status;
    my $qmpstatus = $status->{qmpstatus} // '';
    die "cannot determine running VM monitor status\n" if ($status->{status} // '') eq 'running' && $qmpstatus eq '';
    die "VM is paused or suspended\n" if $qmpstatus ne '' && $qmpstatus ne 'running' && $qmpstatus ne 'stopped';
}

sub disks {
    my ($conf) = @_;
    my $storage = PVE::Storage::config();
    my @disks;
    my %seen;
    for my $key (sort keys %$conf) {
        next if $key !~ /\A(?:ide|sata|scsi|virtio)[0-9]+\z/;
        my $drive = PVE::QemuConfig->parse_volume($key, $conf->{$key});
        next if ($drive->{media} // '') eq 'cdrom';
        my $volume = $drive->{file} // die "missing disk for $key\n";
        next if $volume eq 'none' || $volume =~ /(?:^|[:\/])(?:vm-\d+-)?cloudinit(?:\.|$)/;
        my ($path) = $volume =~ m{\A/} ? ($volume) : PVE::Storage::path($storage, $volume);
        die "disk $key is not an accessible local file or block device\n" if !defined($path) || $path !~ m{\A/} || (!-f $path && !-b $path) || !-r $path;
        next if $seen{$path}++;
        my $format = $drive->{format};
        if (!defined($format)) {
            if (-b $path) { $format = 'raw'; }
            else {
                my $output = '';
                PVE::Tools::run_command(['qemu-img', 'info', '--force-share', '--output=json', $path], outfunc => sub { $output .= $_[0]; });
                $format = decode_json($output)->{format};
            }
        }
        die "unsupported disk format for $key\n" if !defined($format) || $format !~ /\A(?:raw|qcow2|vmdk)\z/;
        push @disks, {path => $path, format => $format};
    }
    die "VM has no accessible attached disks\n" if !@disks;
    return \@disks;
}

sub secure_owner_directory {
    for my $dir ('/var/lib/pvedisk', $owner_dir) {
        if (!-e $dir) { mkdir($dir, 0700) or die "create $dir: $!\n"; }
        my @st = lstat($dir);
        die "unsafe owner directory $dir\n" if !@st || !-d _ || -l _ || $st[4] != 0 || ($st[2] & 0077);
    }
}

sub config_is_absent {
    my $path = PVE::QemuConfig->config_file($vmid);
    my @st = lstat($path);
    return 0 if @st;
    die "cannot establish VM configuration absence: $!\n" if !$!{ENOENT};
    # A local missing file may mean migration or an unavailable pmxcfs rather
    # than deletion. Require a fresh, healthy cluster inventory before cleanup.
    PVE::Cluster::check_cfs_is_mounted();
    PVE::Cluster::check_cfs_quorum();
    PVE::Cluster::cfs_update(1);
    my $vmlist = PVE::Cluster::get_vmlist();
    die "cannot establish cluster-wide VM configuration absence\n" if ref($vmlist) ne 'HASH' || !defined($vmlist->{version}) || $vmlist->{version} !~ /\A[0-9]+\z/ || (exists($vmlist->{ids}) && ref($vmlist->{ids}) ne 'HASH');
    my $ids = $vmlist->{ids} // {};
    die "VM configuration exists on another node; ownership retained\n" if exists $ids->{$vmid};
    @st = lstat($path);
    return 0 if @st;
    die "cannot recheck VM configuration absence: $!\n" if !$!{ENOENT};
    die "VM unexpectedly running; ownership retained\n" if PVE::QemuServer::check_running($vmid, 1);
    return 1;
}

my $result = [];
PVE::QemuConfig->lock_config($vmid, sub {
    my $absent = $operation eq 'release' && config_is_absent();
    my $conf = $absent ? undef : PVE::QemuConfig->load_config($vmid);
    if ($operation eq 'stopped') {
        my $status = PVE::QemuServer::vmstatus($vmid, 1)->{$vmid};
        die "cannot confirm VM $vmid is stopped\n" if !$status || ($status->{status} // '') !~ /\A(?:running|stopped)\z/;
        die "VM $vmid is running; stop it first with: qm shutdown $vmid\n" if $status->{status} ne 'stopped' || PVE::QemuServer::check_running($vmid);
        die "VM $vmid has paused or suspended state\n" if $conf->{vmstate} || ($status->{qmpstatus} // 'stopped') ne 'stopped';
        return;
    }
    if ($operation eq 'release') {
        secure_owner_directory();
        my $fh;
        if (!sysopen($fh, $owner_path, O_RDONLY | O_NOFOLLOW)) {
            # A failed acquisition or completed release has no protection to
            # remove. Any existing Proxmox lock still requires ownership proof.
            return if $!{ENOENT} && ($absent ? config_is_absent() : !defined($conf->{lock}) && !PVE::QemuServer::check_running($vmid));
            die "ownership uncertain; lock retained: $!\n";
        }
        my @st = stat($fh);
        die "unsafe ownership file; lock retained\n" if !@st || !-f _ || $st[4] != 0 || ($st[2] & 0077);
        local $/;
        my $owner = decode_json(<$fh>);
        close($fh) or die "close ownership file: $!\n";
        die "ownership mismatch; record retained\n" if ref($owner) ne 'HASH' || ($owner->{token} // '') ne $token || ($owner->{digest} // '') !~ /\A[0-9a-f]{64}\z/;
        if ($absent) {
            die "VM configuration was recreated; ownership retained\n" if !config_is_absent();
            unlink($owner_path) or die "remove deleted VM ownership record: $!\n";
            return;
        }
        if (!defined($conf->{lock})) {
            # With no Proxmox lock there is nothing to unlock. After session
            # cleanup has checked disk users, discard only our stale marker;
            # the VM may legitimately have been edited or recreated meanwhile.
            die "VM unexpectedly running; ownership retained\n" if PVE::QemuServer::check_running($vmid);
            unlink($owner_path) or die "remove interrupted ownership record: $!\n";
            return;
        }
        die "ownership mismatch; lock retained\n" if $owner->{token} ne $token || ($conf->{lock} // '') ne 'create' || $owner->{digest} ne config_fingerprint($conf);
        die "VM unexpectedly running; lock retained\n" if PVE::QemuServer::check_running($vmid);
        delete $conf->{lock};
        PVE::QemuConfig->write_config($vmid, $conf);
        unlink($owner_path) or die "remove ownership record: $!\n";
        return;
    }
    validate_config($conf);
    $result = disks($conf);
    return if $operation eq 'inspect';
    die "VM is still running\n" if PVE::QemuServer::check_running($vmid);
    secure_owner_directory();
    $conf->{lock} = 'create';
    # Write intent before the lock. An interrupted transition retains evidence and
    # cannot accidentally claim another operation's lock on a later attempt.
    sysopen(my $fh, $owner_path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0600) or die "unresolved ownership record: $!\n";
    print $fh $json->encode({token => $token, digest => config_fingerprint($conf)}) or die "write ownership: $!\n";
    $fh->sync() or die "sync ownership: $!\n";
    close($fh) or die "close ownership: $!\n";
    PVE::QemuConfig->write_config($vmid, $conf);
});
print $json->encode($result), "\n";
