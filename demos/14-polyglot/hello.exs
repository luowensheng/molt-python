name = case System.argv() do
  [n | _] -> n
  []      -> "World"
end
IO.puts "Hello from Elixir #{System.version()}! 👋 #{name}"
