NanoVGo
=============

Pure golang implementation of `NanoVG <https://github.com/memononen/nanovg>`_. NanoVG is a vector graphics engine inspired by HTML5 Canvas API.

`DEMO <https://shibukawa.github.io/nanovgo/>`_

API Reference
---------------

See `GoDoc <https://godoc.org/github.com/shibukawa/nanovgo>`_

Sharp filled tips
-----------------

The GL 3.3 / ES 3.0 renderer uses polygon pixel-area coverage for sharp convex
turns where the usual half-pixel inset and feather can overlap. This reuses the
existing stencil passes and paint shader; it does not allocate a bitmap, texture
or offscreen target. Gradients, images, scissoring and compositing use the same
coverage as solid colors. Ordinary paths keep the existing rendering path.

This correction is limited to simple, disjoint solid contours with at most 128
flattened edges, fitting the guaranteed fragment-uniform budget. Intersections,
touching or nested contours, holes, larger paths and the legacy WebGL shader keep
the original stencil/fringe algorithm. Area is calculated from the flattened
polygon, so curve approximation still depends on the normal tessellation tolerance.
It is not a new promise of exact coverage for arbitrary winding arrangements.

The underlying inset limitation is described in
`NanoVG issue 415 <https://github.com/memononen/nanovg/issues/415>`_.

Porting Memo
--------------

* Root folder ``.go`` files

  Ported from NanoVG.

* ``gl``

  Ported pure-go opengl, includes only needed functions.

* ``fontstashmini/fontstash_mini.go``

  Ported from `fontstash <https://github.com/memononen/fontstash>`_. It includes only needed functions.

* ``fontstashmini/truetype``

  Copy from ``https://github.com/TheOnly92/fontstash.go`` (Public Domain)

License
----------

zlib license

Original (NanoVG) Author
---------------------------

* `Mikko Mononen <https://github.com/memononen>`_

Author
---------------

* `Yoshiki Shibukawa <https://github.com/shibukawa>`_

Contribution
----------------

* Moriyoshi Koizumi
* @hnakamur2
* @mattn_jp
* @hagat
* @h_doxas
* FSX
