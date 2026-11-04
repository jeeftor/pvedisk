package PVE::HA::Config;
use strict;
use warnings;
sub vm_is_ha_managed { return PVE::QemuConfig->load_config($_[0])->{test_ha}; }
1;
