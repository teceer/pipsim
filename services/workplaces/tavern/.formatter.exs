# gen/elixir is deliberately absent from :inputs. It is generated, it is
# formatted by protoc-gen-elixir, and reformatting it here would put this
# project's opinion into a tree four other services read.
[
  inputs: ["{mix,.formatter}.exs", "{config,lib,test}/**/*.{ex,exs}"],
  line_length: 98
]
