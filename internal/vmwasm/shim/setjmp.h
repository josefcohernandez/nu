/* Stub que SOMBREA el setjmp.h de glibc (este wasi-libc no trae ninguno y el
   fallback del include path pescaba el del host). Nadie lo usa: el desenrollado
   va por el trampolín de spike_unwind.h. */
#ifndef SPIKE_FAKE_SETJMP_H
#define SPIKE_FAKE_SETJMP_H
#endif
