email = ENV.fetch("ADMIN_EMAIL")
password = ENV.fetch("ADMIN_PASSWORD")

user = User.find_or_initialize_by(email: email)
user.password = password
user.save!

puts "Seeded user #{email}"
