#version 450
// Reads the staged B8G8R8A8 pixel under the fragment: premultiplied, as
// Wayland ARGB buffers are, so the pipeline blends ONE, ONE_MINUS_SRC_ALPHA.

struct Draw {
    ivec4 rect;
    uvec4 pixels;
};

layout(std430, set = 0, binding = 0) readonly buffer Pixels { uint pixels[]; };
layout(std430, set = 0, binding = 1) readonly buffer Draws { Draw draws[]; };

layout(location = 0) flat in uint draw;
layout(location = 0) out vec4 color;

void main() {
    Draw d = draws[draw];
    ivec2 p = ivec2(gl_FragCoord.xy) - d.rect.xy;
    // Little-endian B,G,R,A bytes: unpack yields (B, G, R, A).
    vec4 bgra = unpackUnorm4x8(pixels[d.pixels.x + uint(p.y) * d.pixels.y + uint(p.x)]);
    color = bgra.zyxw;
}
