# Local patches to github.com/soypat/fat v0.2.1

Source: https://github.com/soypat/fat at tag v0.2.1 (commit 19a3d97d015c9171e5409db176a514fd0cd154c8),
BSD 3-Clause (see LICENSE). Tests and testdata are left out. Used through a `replace` directive in
the top-level go.mod until the fixes are released upstream.

1. `fat.go` `f_sync`: the last-access date (a 16-bit field at offset 18 of a directory entry) was
   written with `PutUint32`, which also zeroed the high 16 bits of the start cluster at offset 20.
   On FAT32 volumes with more than 65535 clusters (every Raspberry Pi OS boot partition), a file
   whose first cluster was above 65535 ended up pointing at the wrong cluster. Now `PutUint16`,
   as in FatFs (`st_word(dir + DIR_LstAccDate, 0)`).
2. `fat.go` `time()`: returns the current local time as a FAT timestamp (FatFs `get_fattime`)
   instead of 0, which decodes as the invalid date 1980-00-00.
