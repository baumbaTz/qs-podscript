# whisper-cli.exe (Vulkan, Windows x64)

whisper.cpp publishes no Windows Vulkan build, so QS-PodScript ships its own,
cross-compiled on Linux (Ubuntu 24.04) with MinGW-w64. Recipe used for this
binary (whisper.cpp v1.9.2):

1. Packages: gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 cmake glslc libvulkan-dev spirv-headers
2. Import library for the driver's vulkan-1.dll:
   curl -O https://raw.githubusercontent.com/KhronosGroup/Vulkan-Loader/main/loader/vulkan-1.def
   x86_64-w64-mingw32-dlltool -d vulkan-1.def -D vulkan-1.dll -l libvulkan-1.a
3. Include dir with vulkan/, vk_video/ and spirv/ copied from /usr/include
4. mingw-compat.h (force-included into C files): defines
   THREAD_POWER_THROTTLING_STATE + constants missing from MinGW's headers.
5. Toolchain file: CMAKE_SYSTEM_NAME Windows, x86_64-w64-mingw32-gcc-posix /
   g++-posix; host toolchain file (gcc/g++) for vulkan-shaders-gen.
6. cmake -B build-win -DCMAKE_TOOLCHAIN_FILE=mingw.cmake
     -DGGML_VULKAN_SHADERS_GEN_TOOLCHAIN=host.cmake -DCMAKE_BUILD_TYPE=Release
     -DBUILD_SHARED_LIBS=OFF -DGGML_VULKAN=ON -DGGML_OPENMP=OFF -DGGML_NATIVE=OFF
     -DGGML_AVX=ON -DGGML_AVX2=ON -DGGML_FMA=ON -DGGML_F16C=ON
     -DWHISPER_BUILD_TESTS=OFF -DWHISPER_SDL2=OFF -DCMAKE_EXE_LINKER_FLAGS=-static
     -DCMAKE_C_FLAGS="-include mingw-compat.h"
     -DVulkan_INCLUDE_DIR=<include dir> -DVulkan_LIBRARY=libvulkan-1.a
     -DVulkan_GLSLC_EXECUTABLE=/usr/bin/glslc
7. Patch ggml/src/ggml-vulkan/CMakeLists.txt: generated shader .cpp files
   get COMPILE_OPTIONS -O0 (pure byte arrays).
8. cmake --build build-win --target whisper-cli -j1
   The generated mul_mm.comp.cpp (120 MB) needs > 4 GB RAM to compile. On a
   small machine: split it at the "const uint64_t" definitions into 4 files
   (each with the #include line), compile each with the same flags, merge with
   x86_64-w64-mingw32-ld -r into
   build-win/ggml/src/ggml-vulkan/CMakeFiles/ggml-vulkan.dir/mul_mm.comp.cpp.obj,
   touch it, continue the build (peak then ~1.2 GB).
9. x86_64-w64-mingw32-strip -> whisper-cli.exe

Result: static exe, only imports vulkan-1.dll (installed by every current
AMD/NVIDIA/Intel driver), KERNEL32, ADVAPI32, msvcrt. CPU code needs AVX2
(Intel Haswell 2013+, AMD Zen). Checked under Wine: starts, Vulkan backend
initializes (no real device there -> falls back to CPU as intended).
