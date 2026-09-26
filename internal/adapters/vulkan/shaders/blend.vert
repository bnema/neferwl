#version 450
// One quad per instance: the instance indexes the draw table.

struct Draw {
    ivec4 rect;   // x0, y0, x1, y1 in target pixels
    uvec4 pixels; // first pixel word in staging, row width, target w, target h
};

layout(std430, set = 0, binding = 1) readonly buffer Draws { Draw draws[]; };

layout(location = 0) flat out uint draw;

void main() {
    Draw d = draws[gl_InstanceIndex];
    // Two triangles: 0,1,2 and 2,1,3 of the corners.
    const ivec2 corners[6] = ivec2[](ivec2(0, 0), ivec2(1, 0), ivec2(0, 1), ivec2(0, 1), ivec2(1, 0), ivec2(1, 1));
    ivec2 c = corners[gl_VertexIndex];
    vec2 p = vec2(c.x == 0 ? d.rect.x : d.rect.z, c.y == 0 ? d.rect.y : d.rect.w);
    gl_Position = vec4(p / vec2(d.pixels.zw) * 2.0 - 1.0, 0.0, 1.0);
    draw = uint(gl_InstanceIndex);
}
