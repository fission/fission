+++
title = "Configuration"
description = ""
weight = 2
+++

When building the website, you can set a theme by using `--theme` option. We suggest you to edit your configuration file and set the theme by default. Example with `config.toml` format.
<!--more-->
```
theme = "docdock"
```

## Search index generation

Add the follow line in the same `config.toml` file.

```
[outputs]
home = [ "HTML", "RSS", "JSON"]
```

LUNRJS search index file will be generated on content changes.

## Your website's content

Find out how to [create]({{%relref "create-page/_index.md"%}}) and [organize your content]({{%relref "content-organisation/_index.md"%}}) quickly and intuitively.