#version 450
// One quad per instance, in the scene's paint order. Draw parameters are
// fetched in the vertex stage and passed flat, not read per fragment.

struct Draw {
    ivec4 rect; // visible x0, y0, x1, y1 in target pixels
    vec4 map; // source texel = map.xy + fragment centre.x * map.zw
    vec4 mapy; // + fragment centre.y * mapy.xy; mapy.z and w are unused
    vec4 color; // solid premultiplied RGBA; content opacity in alpha
    vec4 crop; // source crop bounds in buffer pixels
    uvec4 buf; // pixel-buffer first word, width, height; YUV metadata in w
    uvec4 misc; // mode, flags, target width, target height
    uvec4 resourceIndex; // slot, outer radius bits, inner radius bits, padding
    vec4 round;
    vec4 roundInner;
};
layout(std430, set = 0, binding = 3) readonly buffer Draws { Draw draws[]; };
layout(location = 0) out Parameters {
    flat vec4 map;
    flat vec4 mapy;
    flat vec4 color;
    flat vec4 crop;
    flat uvec4 buf;
    flat uvec4 misc;
    flat uint resourceIndex;
    flat vec4 round;
    flat vec4 roundInner;
    flat vec2 radii;
} parameters;

void main() {
    Draw d = draws[gl_InstanceIndex];
    const ivec2 corners[6] = ivec2[](ivec2(0, 0), ivec2(1, 0), ivec2(0, 1), ivec2(0, 1), ivec2(1, 0), ivec2(1, 1));
    ivec2 c = corners[gl_VertexIndex];
    vec2 p = vec2(c.x == 0 ? d.rect.x : d.rect.z, c.y == 0 ? d.rect.y : d.rect.w);
    gl_Position = vec4(p / vec2(d.misc.zw) * 2.0 - 1.0, 0.0, 1.0);
    parameters.map = d.map;
    parameters.mapy = d.mapy;
    parameters.color = d.color;
    parameters.crop = d.crop;
    parameters.buf = d.buf;
    parameters.misc = d.misc;
    parameters.resourceIndex = d.resourceIndex.x;
    parameters.round = d.round;
    parameters.roundInner = d.roundInner;
    parameters.radii = uintBitsToFloat(d.resourceIndex.yz);
}
