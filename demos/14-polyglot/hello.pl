#!/usr/bin/env perl
my $name = $ARGV[0] // "World";
printf "Hello from Perl %s! 👋 %s\n", $^V, $name;
