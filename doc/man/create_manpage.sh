#!/bin/bash
# Create a manpage from the README.md

# Add header
echo '''% fakemachine(1)

# NAME

fakemachine - fake a machine
''' > fakemachine.md

# Add README.md
tail -n +2 ../../README.md >> fakemachine.md

# Some tweaks to the markdown
# Uppercase titles
sed -i 's/^\(##.*\)$/\U\1/' fakemachine.md

# Remove double #
sed -i 's/^\##/#/' fakemachine.md

# Create the manpage
pandoc -s -t man fakemachine.md -o fakemachine.1

# Resulting manpage can be browsed with groff:
#groff -man -Tascii fakemachine.1
