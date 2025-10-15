# gedcom2jsoncanvas

This was a work in progress I never completed. Generated JSONCanvas (compatible with Obsidian) from ancestry .ged files. Got basic descendants and ancestors working, but couldn't get descendants with spouses to work. You can see some attempts at that in the commit history, before I realized I committed code that regressed the core functionality and reverted it.

Here is an example of the ancestor mapping:

<img width="2501" height="1539" alt="image" src="https://github.com/user-attachments/assets/7d3a27e9-c3de-43db-b42f-bc82afe312f9" />

## Getting Started
To generate the example ancestor mapping yourself from the test.ged file, use: 

```
go run main.go -ged test.ged -root I54 -mode anc
```
