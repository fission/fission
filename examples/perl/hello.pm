=pod

You return a CODEREF from the package that will be called as your function.

The code in the server is something like this (simplified):

    my $sub = require('hello.pm');
    $sub->(request);

As you can see, you get the L<Dancer2::Core::Request> object as first argument
to your function. You can use it to retrieve params given to your function or
anything else Dancer2 offers. You can also use Dancer2 and use its DSL.

=cut

use utf8;
use strict;
use warnings;

# Get more helper functions (status and send_as below)
use Dancer2;

return sub {
    my ($request) = @_;

    my ($name) = $request->query_parameters->{'name'} // 'world';

    # set status code by name
    status 'i_m_a_teapot';

    # send message as JSON
    send_as JSON => {
        msg => "Hello, $name",
        auth => $request->header('Authorization'), # read request header
    };
};
