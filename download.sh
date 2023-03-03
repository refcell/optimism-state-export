#! /bin/bash

# Download the latest data dumps
wget -c https://github.com/testinprod-io/erigon/blob/pcw109550/state-import/state-import/genesis.json

# Download RLP encoded block
wget -c https://drive.google.com/u/0/uc?id=1z1pGEhy8acPi_U-6Sz0oo_-zJSzU8zb-&export=download

# Download RLP encoded receipt
wget -c https://drive.google.com/u/0/uc?id=1QJpv-SNv6I3j9z4FfHzZ3fHlCuFMn8b0&export=download

# Download World State trie
wget -c https://drive.google.com/u/0/uc?id=1k9yopW6F8SyHAR-8JT2hfxptQGT-DqKe&export=download

