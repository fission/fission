+++
title = "Customize website look and feel"
Weight=3
+++

You can change the style and behavior of the theme without touching it.

* inject your own html,  css or js into the page
* overide existing css or js with your own files

## Inject your HTML

### into the \<head\> part of each page :

Create a `custom-head.html` into a `layouts/partials` folder next to the content folder

> * content/
> * layouts/
>   * partials/
>      * custom-head.html

now feel free to add the JS, CSS, HTML code you want :)

### at the end of the body part of each page :

Create a `custom-footer.html` into a `layouts/partials` folder next to the content folder

> * content/
> * layouts/
>   * partials/
>      * custom-footer.html

now feel free to add the JS, CSS, HTML code you want :)

## overide existing CSS or JS

Create the matching file in your static folder, hugo will use yours instead of the theme's one.
Example : 

create a theme.css and place it into `static/css/` to fully overide docdock's theme.css
