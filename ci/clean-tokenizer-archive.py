#!/usr/bin/env python3
"""Remove empty Clang linker-option sections rejected by Go 1.27.4."""

import re
import subprocess
import sys

archive = sys.argv[1]
sections = subprocess.check_output(['readelf', '-SW', archive], text=True)
pattern = re.compile(
    r'\[\s*\d+\]\s+(\S+)\s+LOOS\+0xfff4c0([14])\s+\S+\s+\S+\s+(\S+)'
)
matched = pattern.findall(sections)
for name, section_type, size in matched:
    if name != '.linker-options' or section_type != '1' or int(size, 16) != 0:
        raise SystemExit(f'Refusing to remove nonempty or unexpected linker section: {name}, type {section_type}, size {size}')

if matched:
    subprocess.run(['objcopy', '--remove-section=.linker-options', archive], check=True)
    subprocess.run(['ranlib', archive], check=True)
print(f'Removed {len(matched)} empty .linker-options sections from {archive}')
