#!/usr/bin/env perl

use utf8;
use strict;
use warnings;

use Getopt::Args;
use Plack::Handler::Twiggy;

opt codepath => (
    isa     => 'Str',
    default => '/userfunc/user',
    comment => 'Path to the user code',
);

opt port => (
    isa     => 'Int',
    default => 8888,
    comment => 'Port to listen on',
);

my $options = optargs();

{
    package App::Fission::Perl;

    use Dancer2;

    our $userfunc;

    post '/specialize' => sub {
        if($userfunc) {
            send_error('Not a generic container', 400);
        }

        $userfunc = require($options->{codepath});
        return '';
    };

    any '/' => sub {
        return $userfunc ? $userfunc->(request) : send_error('Not yet specialized', 500);
    };
}

my $handler = Plack::Handler::Twiggy->new(port => $options->{port});
$handler->run(App::Fission::Perl->to_app);
