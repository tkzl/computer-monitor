package com.qianwen.monitor;

// After assembleDebug, compile/run against app/build/tmp/kotlin-classes/debug; no dependencies.
public final class BouncingAxisCheck {
    private static void check(float start, float speed, float limit, float seconds,
                              float expectedPosition, float expectedSpeed) {
        BouncingAxis axis = new BouncingAxis(speed);
        axis.setPosition(start);
        axis.advance(limit, seconds);
        if (Math.abs(axis.getPosition() - expectedPosition) > 0.001f ||
                Math.abs(axis.getVelocity() - expectedSpeed) > 0.001f) {
            throw new AssertionError("Unexpected bounce: " + axis.getPosition() + ", " + axis.getVelocity());
        }
    }

    public static void main(String[] args) {
        check(5, 2, 10, 1, 7, 2);
        check(9, 2, 10, 1, 9, -2);      // Right/bottom edge.
        check(1, -2, 10, 1, 1, 2);      // Left/top edge.
        check(8, 2, 10, 1, 10, -2);     // Exact boundary.
        check(2, -2, 10, 1, 0, 2);
        check(2, 35, 10, 1, 3, -35);    // Multiple reflections.
        check(2, -35, 10, 1, 7, -35);
        check(20, 2, 10, 1, 8, -2);     // Screen shrinks.
        check(5, -2, 0, 1, 0, -2);      // No room to move.
        BouncingAxis axis = new BouncingAxis(-32);
        axis.setPosition(487);
        for (int frame = 0; frame < 100_000; frame++) {
            float limit = frame < 50_000 ? 500 : 137;
            axis.advance(limit, 0.032f);
            if (!Float.isFinite(axis.getPosition()) || axis.getPosition() < 0 || axis.getPosition() > limit) {
                throw new AssertionError("Clock escaped bounds at frame " + frame);
            }
        }
        System.out.println("Bounce checks passed (edges, resize, zero bounds, 100000 frames).");
    }
}
