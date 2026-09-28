#version 450
// One quad per draw: two triangles over the visible target rect.

layout(push_constant) uniform Draw {
    ivec4 rect;  // visible x0, y0, x1, y1 in target pixels
    vec4 map;    // source texel = map.xy + fragment centre.x * map.zw
    vec4 mapy;   //   + fragment centre.y * mapy.xy (zw unused): any buffer transform
    vec4 color;  // solid fills: premultiplied RGBA
    vec4 crop;   // source crop bounds in buffer pixels
    uvec4 buf;   // buffer draws: first word, row width, height, unused
    uvec4 misc;  // mode, flags, target width, target height
} d;

void main() {
    const ivec2 corners[6] = ivec2[](ivec2(0, 0), ivec2(1, 0), ivec2(0, 1), ivec2(0, 1), ivec2(1, 0), ivec2(1, 1));
    ivec2 c = corners[gl_VertexIndex];
    vec2 p = vec2(c.x == 0 ? d.rect.x : d.rect.z, c.y == 0 ? d.rect.y : d.rect.w);
    gl_Position = vec4(p / vec2(d.misc.zw) * 2.0 - 1.0, 0.0, 1.0);
}
