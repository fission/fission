=pod

You return a CODEREF from the package that will be called as your function.

The code in the server is something like this (simplified):

    my $sub = require('hello.pm');
    $sub->(request);

As you can see, you get the L<Dancer2::Core::Request> object as first argument
to your function. You can use it to retrieve params given to your function or
anything else Dancer2 offers. You can also use Dancer2 and use its DSL.

=cut

return sub {
    return 'Hello, ' . (shift->param('name') // 'world');
};
