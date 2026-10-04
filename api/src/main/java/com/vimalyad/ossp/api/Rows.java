package com.vimalyad.ossp.api;

import java.sql.Array;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.time.OffsetDateTime;
import java.util.Arrays;
import java.util.List;

/**
 * Column readers for the types JDBC makes awkward.
 *
 * <p>{@code getInt} returns 0 for NULL, and in this schema NULL means "not
 * observed yet", which a dashboard must not show as zero failing checks.
 */
public final class Rows {
    private Rows() {}

    public static Integer integer(ResultSet rs, String col) throws SQLException {
        return rs.getObject(col, Integer.class);
    }

    public static OffsetDateTime ts(ResultSet rs, String col) throws SQLException {
        return rs.getObject(col, OffsetDateTime.class);
    }

    public static List<String> strings(ResultSet rs, String col) throws SQLException {
        Array a = rs.getArray(col);
        if (a == null) {
            return List.of();
        }
        Object[] values = (Object[]) a.getArray();
        return Arrays.stream(values).map(String::valueOf).toList();
    }
}
